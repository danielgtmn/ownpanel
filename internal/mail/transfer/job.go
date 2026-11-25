package transfer

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Reasons a job stops early.
var (
	errMailboxGone = errors.New("the mailbox no longer exists")
	errQuota       = errors.New("the mailbox is full (quota reached); free space or raise the quota, then import again")
	errDisk        = errors.New("the server's disk is nearly full; the import stopped")
)

// errPaused: the other server limits downloads; the job pauses.
type errPaused struct{ msg string }

func (e *errPaused) Error() string { return e.msg }

// runner is a job while it runs.
type runner struct {
	m   *Manager
	ctx context.Context
	j   config.ImportJob
	tmp *os.File

	host     string // our mail host name
	maxSize  int64
	lastSave time.Time
	folder   int // index into j.Folders of the folder being copied
	local    localFolders
	created  map[string]bool // local folders known to exist
}

// run runs a job to its end and records the outcome.
func (m *Manager) run(ctx context.Context, a *active, j config.ImportJob) {
	st := m.opts.Store
	j.State, j.Error = config.ImportRunning, ""
	if j.Started.IsZero() {
		j.Started = m.now()
	}
	if err := st.SaveImport(ctx, &j); err != nil {
		m.log.Error("mail: import", "job", j.ID, "err", err)
		return
	}
	r := &runner{m: m, ctx: ctx, j: j, created: map[string]bool{}}
	err := r.start()
	if err == nil {
		if j.Kind == config.ImportIMAP {
			m.mu.Lock()
			pw := m.passwords[j.ID]
			m.mu.Unlock()
			err = r.runIMAP(pw)
		} else {
			err = r.runFile()
		}
	}
	if r.tmp != nil {
		r.tmp.Close()
		os.Remove(r.tmp.Name())
	}
	j = r.j
	reason := m.reason(a)
	var pe *errPaused
	event := ""
	switch {
	case reason == stopShutdown:
		// The job stays running in mail.db; Start interrupts or continues it.
		st.SaveImport(context.Background(), &j)
		return
	case reason == stopUser || reason == stopMailbox:
		j.State = config.ImportCancelled
	case err == nil:
		j.State, event = config.ImportDone, config.EventImportFinished
	case errors.As(err, &pe):
		j.State, j.Error, event = config.ImportPaused, pe.msg, config.EventImportPaused
	case errors.Is(err, context.Canceled):
		j.State = config.ImportCancelled
	default:
		j.State, j.Error, event = config.ImportFailed, errText(err), config.EventImportFailed
		if !isPublicErr(err) {
			m.log.Error("mail: import failed", "job", j.ID, "storage_id", j.StorageID, "err", err)
		}
	}
	if j.State != config.ImportPaused {
		j.Finished = m.now()
	}
	m.mu.Lock()
	delete(m.passwords, j.ID)
	m.mu.Unlock()
	if config.ImportFinished(j.State) {
		os.Remove(m.spoolPath(j.ID))
	}
	if err := st.SaveImport(context.Background(), &j); err != nil {
		if !errors.Is(err, storage.ErrNoImport) {
			m.log.Error("mail: import", "job", j.ID, "err", err)
		}
		return
	}
	if event != "" {
		m.emit(j, event)
	}
	m.log.Info("mail: import ended", "job", j.ID, "storage_id", j.StorageID, "state", j.State,
		"copied", j.Copied, "skipped", j.Skipped, "failed", j.Failed)
}

// isPublicErr tells whether err is meant for people as it is.
func isPublicErr(err error) bool {
	var e *Error
	return errors.As(err, &e) || errors.Is(err, errMailboxGone) || errors.Is(err, errQuota) || errors.Is(err, errDisk)
}

// errText is the text of a job's error; internal errors are not shown in
// detail.
func errText(err error) string {
	if isPublicErr(err) {
		return shorten(err.Error(), 200)
	}
	var uf errUnknownFolder
	if errors.As(err, &uf) {
		return shorten(uf.Error(), 200)
	}
	return "the import failed because of an error in the mail service"
}

func (r *runner) start() error {
	if _, ok := r.m.opts.Directory().MailboxByStorageID(r.j.StorageID); !ok {
		return errMailboxGone
	}
	d := r.m.opts.Directory()
	r.host = d.Hostname()
	r.maxSize = d.Limits().MaxMessageBytes
	f, err := os.CreateTemp(r.m.opts.Store.TempDir(), "import-*")
	if err != nil {
		return err
	}
	r.tmp = f
	r.local, err = r.m.localFolders(r.ctx, r.j.StorageID)
	return err
}

// save writes the progress now and then (always when force).
func (r *runner) save(force bool) error {
	if !force && time.Since(r.lastSave) < saveEvery {
		return nil
	}
	r.lastSave = time.Now()
	err := r.m.opts.Store.SaveImport(context.WithoutCancel(r.ctx), &r.j)
	if errors.Is(err, storage.ErrNoImport) {
		return errMailboxGone
	}
	return err
}

// beginFolder adds (or finds) the progress entry of a source folder.
func (r *runner) beginFolder(remote, local string) {
	for i, f := range r.j.Folders {
		if f.Remote == remote && f.Local == local {
			r.folder = i
			return
		}
	}
	r.j.Folders = append(r.j.Folders, config.ImportFolderProgress{Remote: remote, Local: local})
	r.folder = len(r.j.Folders) - 1
}

func (r *runner) endFolder() error {
	r.j.Folders[r.folder].Done = true
	return r.save(true)
}

// ensureFolder creates a local folder (subscribed, with its special use)
// unless it exists. subscribed is nil for "yes".
func (r *runner) ensureFolder(name, use string, subscribed *bool) error {
	if r.created[name] {
		return nil
	}
	if err := r.beforeWrite(0); err != nil {
		return err
	}
	st := r.m.opts.Store
	err := st.CreateFolder(r.ctx, r.j.StorageID, name)
	switch {
	case errors.Is(err, storage.ErrFolderExists):
		// Our manifest says whether it is subscribed.
		if subscribed != nil {
			if err := st.SetSubscribed(r.ctx, r.j.StorageID, name, *subscribed); err != nil {
				return err
			}
		}
	case err != nil:
		return err
	default:
		sub := subscribed == nil || *subscribed
		if err := st.SetSubscribed(r.ctx, r.j.StorageID, name, sub); err != nil {
			return err
		}
		if use != "" {
			if err := st.SetSpecialUse(r.ctx, r.j.StorageID, name, use); err != nil {
				return err
			}
		}
	}
	r.created[name] = true
	return nil
}

// beforeWrite checks, before every message, that the job may go on: not
// cancelled, the mailbox still configured, the quota not reached and
// enough disk space left for size more bytes.
func (r *runner) beforeWrite(size int64) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	mb, ok := r.m.opts.Directory().MailboxByStorageID(r.j.StorageID)
	if !ok {
		return errMailboxGone
	}
	if mb.QuotaBytes > 0 {
		u, err := r.m.opts.Store.Usage(r.ctx, r.j.StorageID)
		if err != nil {
			return err
		}
		if u.Bytes+size > mb.QuotaBytes {
			return errQuota
		}
	}
	free, err := r.m.opts.FreeBytes()
	if err != nil {
		return err
	}
	if free-size < r.m.opts.MinFreeBytes {
		return errDisk
	}
	return nil
}

// incoming is a message read from a source.
type incoming struct {
	body  io.Reader
	flags []string  // from the source (IMAP); file sources read headers
	date  time.Time // received date if the source knows it
	meta  bool      // read mbox metadata fields
	size  int64     // expected size if known (0: unknown)
	// wm moves with the message when it is copied (IMAP).
	wm *storage.Watermark
}

// outcome of put.
type outcome int

const (
	copied outcome = iota
	skipped
	failed
)

// put stores one message in local folder (anyFolder: skip it if it was
// imported into any folder before). It returns the prepared message (for
// file sources that decide the folder from it) and the outcome; errors
// stop the job.
func (r *runner) put(local string, anyFolder bool, in incoming, remote string) (outcome, *prepared, error) {
	if err := r.beforeWrite(in.size); err != nil {
		return failed, nil, err
	}
	p, err := r.prepare(in)
	if err != nil {
		var reason string
		switch {
		case errors.Is(err, errTooLarge):
			reason = "larger than the message size limit"
		case errors.Is(err, errHeaderTooLong):
			reason = "header too large"
		default:
			return failed, nil, err
		}
		return failed, nil, r.fail(remote, nil, reason)
	}
	return r.store(local, anyFolder, in, p, remote)
}

func (r *runner) prepare(in incoming) (*prepared, error) {
	if _, err := r.tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := r.tmp.Truncate(0); err != nil {
		return nil, err
	}
	return prepare(in.body, r.tmp, prepOptions{host: r.host, meta: in.meta, max: r.maxSize})
}

// store appends a prepared message (in r.tmp).
func (r *runner) store(local string, anyFolder bool, in incoming, p *prepared, remote string) (outcome, *prepared, error) {
	st := r.m.opts.Store
	sid := r.j.StorageID
	if p.deleted {
		return skipped, p, nil
	}
	if err := r.beforeWrite(p.size); err != nil {
		return failed, p, err
	}
	where := local
	if anyFolder {
		where = ""
	}
	seen, err := st.ImportSeen(r.ctx, sid, where, p.key)
	if err != nil {
		return failed, p, err
	}
	if seen {
		r.j.Skipped++
		r.j.Folders[r.folder].Skipped++
		return skipped, p, r.save(false)
	}
	if _, err := r.tmp.Seek(0, io.SeekStart); err != nil {
		return failed, p, err
	}
	flags := in.flags
	date := in.date
	if date.IsZero() {
		date = p.received
	}
	if date.IsZero() {
		date = p.date
	}
	_, err = st.Append(r.ctx, sid, local, io.LimitReader(r.tmp, p.size), storage.AppendOptions{
		Flags: flags, Date: date, MaxSize: r.maxSize,
	})
	switch {
	case errors.Is(err, storage.ErrOverQuota):
		return failed, p, errQuota
	case errors.Is(err, storage.ErrTooLarge):
		return failed, p, r.fail(remote, p, "larger than the message size limit")
	case errors.Is(err, storage.ErrInvalidFlag):
		return failed, p, r.fail(remote, p, "invalid flags")
	case err != nil:
		return failed, p, err
	}
	if err := st.RecordImported(context.WithoutCancel(r.ctx), sid, local, p.key, in.wm); err != nil {
		return failed, p, err
	}
	r.j.Copied++
	r.j.Bytes += p.size
	r.j.Folders[r.folder].Copied++
	return copied, p, r.save(false)
}

// fail records a message that was not copied; the job goes on.
func (r *runner) fail(remote string, p *prepared, reason string) error {
	f := config.ImportFailure{Folder: remote, Reason: reason}
	if p != nil {
		f.Subject = p.subject
		f.Date = p.date
		if f.Date.IsZero() {
			f.Date = p.received
		}
		f.Date = f.Date.UTC()
	}
	r.j.Failed++
	r.j.Folders[r.folder].Failed++
	if err := r.m.opts.Store.AddImportFailure(context.WithoutCancel(r.ctx), r.j.ID, f); err != nil {
		return err
	}
	return r.save(false)
}

// failRaw records a failure for which only a subject and date are known.
func (r *runner) failRaw(remote, subject string, date time.Time, reason string) error {
	return r.fail(remote, &prepared{subject: shorten(subject, 80), date: date}, reason)
}
