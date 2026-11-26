// Package transfer imports mail into mailboxes (from another account over
// IMAP, or from an uploaded mbox, eml or zip file) and exports a mailbox
// as a zip file. It runs in the mail service, the only process that may
// read and write mail data; the panel checks permissions and drives it
// through the admin socket (see config.Client). The design is "Import and
// export" in wiki/E-Mail-Architecture.md.
//
// Jobs are rows in mail.db (storage). At most MaxRunning jobs run at
// once, one per mailbox; the others wait. Scans of uploaded files are
// bounded the same way, separately (at most MaxRunning at once). The password of an IMAP job is
// kept only in memory: after a restart such a job is interrupted and
// continues once the password is given again. File jobs read their
// upload from a spool file in imports/ and continue by themselves.
package transfer

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// DefaultMaxRunning is how many jobs run at once.
const DefaultMaxRunning = 2

// maxScans is how often the scan of an upload starts before it is given
// up: a scan the mail service did not survive (it was killed while
// reading the file) is tried once more after the restart, not forever.
const maxScans = 2

// saveEvery is how often a running job writes its progress.
const saveEvery = time.Second

// Resolver looks up addresses; *net.Resolver is one.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Options configure a Manager.
type Options struct {
	Store *storage.Store
	// Directory returns the current configuration view.
	Directory func() *directory.Directory
	// FreeBytes reports free disk space; nil means Store.FreeBytes.
	FreeBytes func() (int64, error)
	// MinFreeBytes must stay free; a job stops before it would go below.
	MinFreeBytes int64
	// Event records an event for the panel; nil drops them.
	Event  func(config.Event)
	Logger *slog.Logger

	// Resolver: nil means net.DefaultResolver.
	Resolver Resolver
	// Dial connects to another IMAP server; nil means a net.Dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// RootCAs verify certificates of other servers; nil means the system's
	// (tests replace it).
	RootCAs *x509.CertPool
	// AllowPrivate allows IMAP servers on private and local addresses
	// (tests and test networks only).
	AllowPrivate bool
	// MaxRunning bounds the jobs that run at once; 0 means
	// DefaultMaxRunning.
	MaxRunning int
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

// Manager runs import jobs and exports.
type Manager struct {
	opts Options
	log  *slog.Logger

	mu        sync.Mutex
	active    map[int64]*active // running or being scanned
	passwords map[int64]string  // IMAP jobs that wait or run
	rate      map[string][]time.Time
	exporting map[string]bool
	uploading map[int64]bool
	started   bool
	stopped   bool

	wg sync.WaitGroup
}

// stop reasons of an active job.
type stopReason int

const (
	stopNone stopReason = iota
	stopUser
	stopMailbox
	stopShutdown
)

type active struct {
	sid      string
	scanning bool
	cancel   context.CancelFunc
	done     chan struct{}
	reason   stopReason
}

// New returns a manager; call Start once the storage is ready.
func New(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.FreeBytes == nil {
		opts.FreeBytes = opts.Store.FreeBytes
	}
	if opts.Event == nil {
		opts.Event = func(config.Event) {}
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Dial == nil {
		d := &net.Dialer{Timeout: dialTimeout}
		opts.Dial = d.DialContext
	}
	if opts.MaxRunning <= 0 {
		opts.MaxRunning = DefaultMaxRunning
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Manager{
		opts: opts, log: opts.Logger,
		active:    map[int64]*active{},
		passwords: map[int64]string{},
		rate:      map[string][]time.Time{},
		exporting: map[string]bool{},
		uploading: map[int64]bool{},
	}
}

func (m *Manager) now() time.Time { return m.opts.Now().UTC().Truncate(time.Second) }

func (m *Manager) spoolPath(id int64) string {
	return filepath.Join(m.opts.Store.ImportsDir(), strconv.FormatInt(id, 10)+".spool")
}

var errUnknownMailbox = errorf(http.StatusNotFound, "unknown mailbox")

func (m *Manager) mailbox(sid string) (*config.Mailbox, error) {
	if !config.ValidStorageID(sid) {
		return nil, errorf(http.StatusBadRequest, "invalid storage id")
	}
	mb, ok := m.opts.Directory().MailboxByStorageID(sid)
	if !ok {
		return nil, errUnknownMailbox
	}
	return mb, nil
}

// allow counts a check or start of a mailbox against ImportRateLimit.
func (m *Manager) allow(sid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.opts.Now()
	var keep []time.Time
	for _, t := range m.rate[sid] {
		if now.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	if len(keep) >= config.ImportRateLimit {
		m.rate[sid] = keep
		return errorf(http.StatusTooManyRequests, "too many imports for this mailbox within an hour; try again later")
	}
	m.rate[sid] = append(keep, now)
	return nil
}

// busy refuses a new job while the mailbox has one that has not finished
// (paused and interrupted jobs do not count).
func (m *Manager) busy(ctx context.Context, sid string) error {
	js, err := m.opts.Store.Imports(ctx, sid)
	if err != nil {
		return err
	}
	for _, j := range js {
		switch j.State {
		case config.ImportUploading, config.ImportChecking, config.ImportReady, config.ImportWaiting, config.ImportRunning:
			return errorf(http.StatusConflict, "another import into this mailbox has not finished yet")
		}
	}
	return nil
}

func (m *Manager) quotaNumbers(ctx context.Context, mb *config.Mailbox, c *config.ImportCheck) error {
	u, err := m.opts.Store.Usage(ctx, mb.StorageID)
	if err != nil {
		return err
	}
	c.QuotaBytes, c.UsedBytes, c.QuotaLeft = mb.QuotaBytes, u.Bytes, -1
	if mb.QuotaBytes > 0 {
		c.QuotaLeft = max(mb.QuotaBytes-u.Bytes, 0)
	}
	return nil
}

func (m *Manager) localFolders(ctx context.Context, sid string) (localFolders, error) {
	fs, err := m.opts.Store.ListFolders(ctx, sid)
	if err != nil {
		return nil, err
	}
	return newLocalFolders(fs), nil
}

// Check signs in to another account and lists what an import would copy.
func (m *Manager) Check(ctx context.Context, sid string, src config.ImportSource) (config.ImportCheck, error) {
	var c config.ImportCheck
	mb, err := m.mailbox(sid)
	if err != nil {
		return c, err
	}
	if src, err = validateSource(src); err != nil {
		return c, err
	}
	if err := m.allow(sid); err != nil {
		return c, err
	}
	s, err := m.connect(ctx, src)
	if err != nil {
		return c, err
	}
	defer s.logout()
	folders, err := s.folders(true)
	if err != nil {
		return c, remoteErr("listing the folders of %s failed: %s", src.Host, s.clean(err))
	}
	local, err := m.localFolders(ctx, sid)
	if err != nil {
		return c, err
	}
	c = makePlan(folders, local).check()
	return c, m.quotaNumbers(ctx, mb, &c)
}

// StartIMAP starts an import from another account; the job waits until
// it may run.
func (m *Manager) StartIMAP(ctx context.Context, sid string, st config.ImportStart) (config.ImportJob, error) {
	var j config.ImportJob
	if _, err := m.mailbox(sid); err != nil {
		return j, err
	}
	src, err := validateSource(st.Source)
	if err != nil {
		return j, err
	}
	if err := m.busy(ctx, sid); err != nil {
		return j, err
	}
	if err := m.allow(sid); err != nil {
		return j, err
	}
	j = config.ImportJob{
		StorageID: sid, Kind: config.ImportIMAP, State: config.ImportWaiting,
		Host: src.Host, Port: src.Port, Username: src.Username, Choices: st.Choices,
		Folders: []config.ImportFolderProgress{},
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.opts.Store.CreateImport(ctx, &j); err != nil {
		return j, err
	}
	m.passwords[j.ID] = src.Password
	m.log.Info("mail: import started", "job", j.ID, "storage_id", sid, "host", src.Host)
	m.scheduleLocked()
	return j, nil
}

// validFileName accepts the name of an uploaded file: at most 255 bytes
// of UTF-8 without control characters or path separators.
func validFileName(n string) bool {
	if n == "" || len(n) > 255 || !utf8.ValidString(n) || strings.ContainsAny(n, `/\`) || n == "." || n == ".." {
		return false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// CreateUpload declares a file to import.
func (m *Manager) CreateUpload(ctx context.Context, sid string, req config.UploadRequest) (config.ImportJob, error) {
	var j config.ImportJob
	if _, err := m.mailbox(sid); err != nil {
		return j, err
	}
	if !validFileName(req.Name) {
		return j, errorf(http.StatusBadRequest, "the file name is not valid")
	}
	if req.Size <= 0 {
		return j, errorf(http.StatusBadRequest, "the file is empty")
	}
	if req.Size > config.MaxImportBytes {
		return j, errorf(http.StatusRequestEntityTooLarge, "the file is larger than %d GiB", config.MaxImportBytes>>30)
	}
	if err := m.enoughSpace(req.Size); err != nil {
		return j, err
	}
	if err := m.busy(ctx, sid); err != nil {
		return j, err
	}
	if err := m.allow(sid); err != nil {
		return j, err
	}
	j = config.ImportJob{
		StorageID: sid, Kind: config.ImportFile, State: config.ImportUploading,
		FileName: req.Name, FileSize: req.Size, Folders: []config.ImportFolderProgress{},
	}
	if err := m.opts.Store.CreateImport(ctx, &j); err != nil {
		return j, err
	}
	f, err := os.OpenFile(m.spoolPath(j.ID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		m.opts.Store.DeleteImport(ctx, sid, j.ID)
		return j, err
	}
	f.Close()
	return j, nil
}

func (m *Manager) enoughSpace(n int64) error {
	free, err := m.opts.FreeBytes()
	if err != nil {
		return err
	}
	if free-n < m.opts.MinFreeBytes {
		return errorf(http.StatusInsufficientStorage, "not enough disk space on the server")
	}
	return nil
}

// WritePart stores a part of an upload that starts at offset and returns
// how many bytes the upload has. Bytes that arrived before a broken
// connection are kept, so the next part goes on from there.
func (m *Manager) WritePart(ctx context.Context, sid string, id, offset int64, body io.Reader, length int64) (int64, error) {
	if _, err := m.mailbox(sid); err != nil {
		return 0, err
	}
	m.mu.Lock()
	if m.uploading[id] {
		m.mu.Unlock()
		return 0, errorf(http.StatusConflict, "another part of this upload is being sent")
	}
	m.uploading[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.uploading, id)
		m.mu.Unlock()
	}()
	j, err := m.job(ctx, sid, id)
	if err != nil {
		return 0, err
	}
	if j.State != config.ImportUploading {
		return j.Received, errorf(http.StatusConflict, "the upload is complete")
	}
	if offset != j.Received {
		return j.Received, errorf(http.StatusConflict, "the part must start at byte %d", j.Received)
	}
	if length <= 0 || length > config.MaxUploadPart {
		return j.Received, errorf(http.StatusRequestEntityTooLarge, "a part must have 1 to %d bytes", config.MaxUploadPart)
	}
	if offset+length > j.FileSize {
		return j.Received, errorf(http.StatusBadRequest, "the part goes beyond the declared size")
	}
	if err := m.enoughSpace(length); err != nil {
		return j.Received, err
	}
	f, err := os.OpenFile(m.spoolPath(id), os.O_WRONLY, 0)
	if err != nil {
		return j.Received, err
	}
	defer f.Close()
	// Bytes of an earlier part beyond what was recorded are dropped.
	if err := f.Truncate(offset); err != nil {
		return j.Received, err
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return j.Received, err
	}
	n, copyErr := copyN(f, body, length)
	if err := f.Sync(); err != nil {
		return j.Received, err
	}
	j.Received = offset + n
	if j.Received == j.FileSize {
		j.State = config.ImportChecking
	}
	if err := m.opts.Store.SaveImport(context.WithoutCancel(ctx), &j); err != nil {
		return j.Received, err
	}
	if copyErr != nil {
		return j.Received, copyErr
	}
	if n < length {
		return j.Received, errorf(http.StatusBadRequest, "the part ended early")
	}
	if j.State == config.ImportChecking {
		m.mu.Lock()
		m.scheduleLocked()
		m.mu.Unlock()
	}
	return j.Received, nil
}

func copyN(f *os.File, r io.Reader, n int64) (int64, error) {
	buf := make([]byte, 256<<10)
	var done int64
	for done < n {
		want := min(int64(len(buf)), n-done)
		k, err := r.Read(buf[:want])
		if k > 0 {
			if _, werr := f.Write(buf[:k]); werr != nil {
				return done, werr
			}
			done += int64(k)
		}
		if err != nil {
			if done == n {
				return done, nil
			}
			if errors.Is(err, io.EOF) {
				return done, nil
			}
			return done, err
		}
	}
	return done, nil
}

// Resume starts a file job that is ready, or continues a paused or
// interrupted IMAP job with its password.
func (m *Manager) Resume(ctx context.Context, sid string, id int64, r config.ImportResume) (config.ImportJob, error) {
	if _, err := m.mailbox(sid); err != nil {
		return config.ImportJob{}, err
	}
	j, err := m.job(ctx, sid, id)
	if err != nil {
		return j, err
	}
	switch {
	case j.Kind == config.ImportFile && j.State == config.ImportReady:
		if j.Check != nil && r.Choices.Folders != nil {
			known := map[string]bool{}
			for _, f := range j.Check.Folders {
				known[f.Remote] = true
			}
			for _, f := range r.Choices.Folders {
				if !known[f] {
					return j, errorf(http.StatusBadRequest, "%s", errUnknownFolder(f).Error())
				}
			}
		}
		j.Choices = r.Choices
	case j.Kind == config.ImportIMAP && (j.State == config.ImportPaused || j.State == config.ImportInterrupted):
		if r.Password == "" || len(r.Password) > maxCredential || strings.ContainsAny(r.Password, "\x00\r\n") {
			return j, errorf(http.StatusBadRequest, "the password is missing or not valid")
		}
	default:
		return j, errorf(http.StatusConflict, "the import cannot be started in its state (%s)", j.State)
	}
	if err := m.allow(sid); err != nil {
		return j, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j.State, j.Error = config.ImportWaiting, ""
	if err := m.opts.Store.SaveImport(ctx, &j); err != nil {
		return j, err
	}
	if j.Kind == config.ImportIMAP {
		m.passwords[j.ID] = r.Password
	}
	m.scheduleLocked()
	return j, nil
}

var errNoJob = errorf(http.StatusNotFound, "unknown import")

func (m *Manager) job(ctx context.Context, sid string, id int64) (config.ImportJob, error) {
	j, err := m.opts.Store.Import(ctx, sid, id)
	if errors.Is(err, storage.ErrNoImport) {
		return j, errNoJob
	}
	return j, err
}

// Jobs lists the jobs of a mailbox, newest first.
func (m *Manager) Jobs(ctx context.Context, sid string) ([]config.ImportJob, error) {
	if _, err := m.mailbox(sid); err != nil {
		return nil, err
	}
	return m.opts.Store.Imports(ctx, sid)
}

// Job returns a job with its failures.
func (m *Manager) Job(ctx context.Context, sid string, id int64) (config.ImportJob, error) {
	if _, err := m.mailbox(sid); err != nil {
		return config.ImportJob{}, err
	}
	j, err := m.job(ctx, sid, id)
	if err != nil {
		return j, err
	}
	j.Failures, err = m.opts.Store.ImportFailures(ctx, id)
	return j, err
}

// Cancel cancels a job that has not finished and waits for it to stop,
// or removes a finished one.
func (m *Manager) Cancel(ctx context.Context, sid string, id int64) error {
	if _, err := m.mailbox(sid); err != nil {
		return err
	}
	j, err := m.job(ctx, sid, id)
	if err != nil {
		return err
	}
	if config.ImportFinished(j.State) {
		os.Remove(m.spoolPath(id))
		return m.opts.Store.DeleteImport(ctx, sid, id)
	}
	m.mu.Lock()
	if m.uploading[id] {
		m.mu.Unlock()
		return errorf(http.StatusConflict, "a part of this upload is being sent; stop it first")
	}
	if a, ok := m.active[id]; ok {
		a.reason = stopUser
		a.cancel()
		m.mu.Unlock()
		select {
		case <-a.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer m.mu.Unlock()
	// Read again under the lock: the scheduler may have started it.
	if j, err = m.job(ctx, sid, id); err != nil {
		return err
	}
	if config.ImportFinished(j.State) {
		return nil
	}
	delete(m.passwords, id)
	os.Remove(m.spoolPath(id))
	j.State, j.Finished = config.ImportCancelled, m.now()
	return m.opts.Store.SaveImport(ctx, &j)
}

// StopMailbox cancels the jobs of a mailbox and waits for them; call it
// before deleting the mailbox's data.
func (m *Manager) StopMailbox(ctx context.Context, sid string) error {
	m.mu.Lock()
	var wait []*active
	for _, a := range m.active {
		if a.sid == sid {
			a.reason = stopMailbox
			a.cancel()
			wait = append(wait, a)
		}
	}
	js, err := m.opts.Store.Imports(ctx, sid)
	if err == nil {
		for _, j := range js {
			delete(m.passwords, j.ID)
			if _, running := m.active[j.ID]; !running && !config.ImportFinished(j.State) {
				j.State, j.Finished = config.ImportCancelled, m.now()
				m.opts.Store.SaveImport(ctx, &j)
			}
		}
	}
	m.mu.Unlock()
	for _, a := range wait {
		select {
		case <-a.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	for _, j := range js {
		os.Remove(m.spoolPath(j.ID))
	}
	return err
}

// Start recovers jobs after a restart and starts waiting ones: IMAP jobs
// that ran or waited are interrupted (their password is gone), file jobs
// continue, uploads that were complete are scanned again, and spool
// files without a job are removed.
func (m *Manager) Start(ctx context.Context) error {
	st := m.opts.Store
	js, err := st.ImportsIn(ctx, config.ImportRunning, config.ImportWaiting, config.ImportChecking)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = true
	for _, j := range js {
		switch {
		case j.Kind == config.ImportIMAP:
			j.State = config.ImportInterrupted
			j.Error = "the mail service restarted; enter the password again to continue"
			if err := st.SaveImport(ctx, &j); err != nil {
				return err
			}
			m.emit(j, config.EventImportInterrupted)
		case j.State == config.ImportRunning:
			j.State = config.ImportWaiting
			if err := st.SaveImport(ctx, &j); err != nil {
				return err
			}
		case j.State == config.ImportChecking && j.Scans >= maxScans:
			// Earlier scans ended with the service, not with a result.
			j.State, j.Finished = config.ImportFailed, m.now()
			j.Error = "the file could not be checked; the mail service stopped while reading it"
			os.Remove(m.spoolPath(j.ID))
			if err := st.SaveImport(ctx, &j); err != nil {
				return err
			}
			m.emit(j, config.EventImportFailed)
		}
	}
	m.removeOrphanSpools(ctx)
	m.pruneLocked(ctx)
	m.scheduleLocked()
	return nil
}

// removeOrphanSpools deletes spool files whose job is gone or finished.
func (m *Manager) removeOrphanSpools(ctx context.Context) {
	ids, err := m.opts.Store.ImportIDs(ctx)
	if err != nil {
		m.log.Error("mail: import spools", "err", err)
		return
	}
	dir := m.opts.Store.ImportsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		m.log.Error("mail: import spools", "err", err)
		return
	}
	for _, e := range entries {
		id, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".spool"), 10, 64)
		state, ok := ids[id]
		if err != nil || !strings.HasSuffix(e.Name(), ".spool") || !ok || config.ImportFinished(state) {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// Prune removes finished jobs older than config.ImportRetention.
func (m *Manager) Prune(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pruneLocked(ctx)
}

func (m *Manager) pruneLocked(ctx context.Context) error {
	ids, err := m.opts.Store.PruneImports(ctx, m.opts.Now().Add(-config.ImportRetention))
	for _, id := range ids {
		os.Remove(m.spoolPath(id))
	}
	return err
}

// Shutdown stops running jobs (they continue or are interrupted after the
// next start) and waits for them, bounded by ctx.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.stopped = true
	for _, a := range m.active {
		a.reason = stopShutdown
		a.cancel()
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// scheduleLocked starts scans of complete uploads and waiting jobs,
// oldest first: at most MaxRunning scans, and at most MaxRunning jobs and
// one per mailbox.
func (m *Manager) scheduleLocked() {
	if !m.started || m.stopped {
		return
	}
	running, scans := 0, 0
	busy := map[string]bool{}
	for _, a := range m.active {
		if a.scanning {
			scans++
		} else {
			running++
			busy[a.sid] = true
		}
	}
	if scans < m.opts.MaxRunning {
		cs, err := m.opts.Store.ImportsIn(context.Background(), config.ImportChecking)
		if err != nil {
			m.log.Error("mail: schedule import scans", "err", err)
		}
		for _, j := range cs {
			if scans >= m.opts.MaxRunning {
				break
			}
			if _, ok := m.active[j.ID]; ok {
				continue
			}
			scans++
			m.startLocked(j, true)
		}
	}
	if running >= m.opts.MaxRunning {
		return
	}
	js, err := m.opts.Store.ImportsIn(context.Background(), config.ImportWaiting)
	if err != nil {
		m.log.Error("mail: schedule imports", "err", err)
		return
	}
	for _, j := range js {
		if running >= m.opts.MaxRunning {
			return
		}
		if busy[j.StorageID] {
			continue
		}
		if j.Kind == config.ImportIMAP {
			if _, ok := m.passwords[j.ID]; !ok {
				j.State = config.ImportInterrupted
				j.Error = "enter the password again to continue"
				m.opts.Store.SaveImport(context.Background(), &j)
				continue
			}
		}
		busy[j.StorageID] = true
		running++
		m.startLocked(j, false)
	}
}

func (m *Manager) startLocked(j config.ImportJob, scan bool) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &active{sid: j.StorageID, scanning: scan, cancel: cancel, done: make(chan struct{})}
	m.active[j.ID] = a
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(a.done)
		defer cancel()
		if scan {
			m.scan(ctx, a, j)
		} else {
			m.run(ctx, a, j)
		}
		m.mu.Lock()
		delete(m.active, j.ID)
		m.scheduleLocked()
		m.mu.Unlock()
	}()
}

func (m *Manager) reason(a *active) stopReason {
	m.mu.Lock()
	defer m.mu.Unlock()
	return a.reason
}

// emit records an event about a job.
func (m *Manager) emit(j config.ImportJob, kind string) {
	src := j.FileName
	if j.Kind == config.ImportIMAP {
		src = fmt.Sprintf("%s at %s", j.Username, j.Host)
	}
	detail := fmt.Sprintf("import %d from %s: %d copied, %d already there, %d failed", j.ID, src, j.Copied, j.Skipped, j.Failed)
	if j.Error != "" {
		detail += ": " + j.Error
	}
	var addr string
	if mb, ok := m.opts.Directory().MailboxByStorageID(j.StorageID); ok {
		addr = mb.Address
	}
	m.opts.Event(config.Event{Kind: kind, StorageID: j.StorageID, Address: addr, Detail: detail})
}
