package panel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Import and export of mailboxes ("Import and export" in
// wiki/E-Mail-Architecture.md). The mail service does the work; the panel
// decides who may and records it.
//
// Who may:
//   - importing and exporting: whoever may change the mailbox (admins, the
//     owner of the mail domain), signed in with a password or with an
//     unrestricted write token: export reads a whole mailbox, import writes
//     into it, so both are write-level;
//   - seeing import jobs: whoever may see the mailbox, read tokens too.
//
// The password of another mail account passes through to the service and
// is never stored, logged or recorded here.

// MailTransfer is what the panel needs from the mail service for import
// and export; *config.Client satisfies it. A MailService without it (test
// doubles) means import and export are not available.
type MailTransfer interface {
	CheckImport(ctx context.Context, storageID string, src config.ImportSource) (config.ImportCheck, error)
	StartImport(ctx context.Context, storageID string, start config.ImportStart) (config.ImportJob, error)
	CreateUpload(ctx context.Context, storageID string, req config.UploadRequest) (config.ImportJob, error)
	UploadPart(ctx context.Context, storageID string, id, offset int64, part io.Reader, size int64) (int64, error)
	ResumeImport(ctx context.Context, storageID string, id int64, r config.ImportResume) (config.ImportJob, error)
	ImportJobs(ctx context.Context, storageID string) ([]config.ImportJob, error)
	ImportJob(ctx context.Context, storageID string, id int64) (config.ImportJob, error)
	CancelImport(ctx context.Context, storageID string, id int64) error
	Export(ctx context.Context, storageID string) (io.ReadCloser, error)
}

var _ MailTransfer = (*config.Client)(nil)

const (
	// importCheckTimeout bounds signing in to another account and listing
	// its folders; it stays below the HTTP server's write limit.
	importCheckTimeout = 4 * time.Minute
	// importsPerPerson is how many checks, starts and uploads one person
	// may begin per hour, over all mailboxes (the service limits each
	// mailbox on its own).
	importsPerPerson = config.ImportRateLimit
	importWindow     = time.Hour
)

// MailImport is an import job as callers see it: the service's job
// without its storage id, with the mailbox's address.
type MailImport struct {
	config.ImportJob
	// StorageID hides the service's internal id of the mailbox.
	StorageID struct{} `json:"storage_id,omitzero"`
	Address   string   `json:"address"`
}

func mailImportView(m store.Mailbox, j config.ImportJob) MailImport {
	j.StorageID = ""
	return MailImport{ImportJob: j, Address: m.LocalPart + "@" + m.Domain}
}

// importRate counts what each person began within importWindow. It is
// kept in memory: a restart forgets it, the service's own limit per
// mailbox stays.
type importRate struct {
	mu   sync.Mutex
	seen map[int64][]time.Time
}

func (r *importRate) allow(user int64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[int64][]time.Time{}
	}
	keep := r.seen[user][:0]
	for _, t := range r.seen[user] {
		if now.Sub(t) < importWindow && !t.After(now) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= importsPerPerson {
		r.seen[user] = keep
		return false
	}
	r.seen[user] = append(keep, now)
	return true
}

// MailboxByAddress returns the id of the mailbox local@domain if the actor
// may see it.
func (s *Service) MailboxByAddress(ctx context.Context, a Actor, domain, local string) (int64, error) {
	md, err := s.visibleMailDomain(ctx, a, domain)
	if err != nil {
		return 0, err
	}
	l := strings.ToLower(strings.TrimSpace(local))
	list, err := s.store.Mailboxes(ctx, md.Domain)
	if err != nil {
		return 0, storeError(err)
	}
	for _, m := range list {
		if m.LocalPart == l && a.canSeeMail(m.OwnerID) {
			return m.ID, nil
		}
	}
	return 0, errorf(NotFound, "mailbox not found")
}

// transfer returns the service's import and export side.
func (s *Service) transfer() (MailTransfer, error) {
	if s.mail == nil {
		return nil, errorf(Conflict, "the mail service is not set up on this server")
	}
	t, ok := s.mail.(MailTransfer)
	if !ok {
		return nil, errorf(Conflict, "this mail service cannot import or export mail")
	}
	return t, nil
}

// transferMailbox checks that the actor may see (write: also change) the
// mailbox, and that the service can import and export.
func (s *Service) transferMailbox(ctx context.Context, a Actor, id int64, write bool) (store.Mailbox, MailTransfer, error) {
	m, err := s.visibleMailbox(ctx, a, id)
	if err != nil {
		return m, nil, err
	}
	if write {
		if err := needMailWrite(a); err != nil {
			return m, nil, err
		}
	}
	t, err := s.transfer()
	return m, t, err
}

// beginImport counts a check, start or upload against the person's limit.
func (s *Service) beginImport(a Actor) error {
	if !s.importRate.allow(a.UserID, s.now()) {
		return errorf(Throttled, "you started too many imports in the last hour; try again later")
	}
	return nil
}

// transferError explains an answer of the mail service. Its texts are
// meant for people and never contain a password.
func transferError(err error, what string) error {
	var e *config.Error
	if !errors.As(err, &e) {
		if errors.Is(err, context.DeadlineExceeded) {
			return &Error{Kind: Unavailable, Msg: what + " took too long; try again later", Err: err}
		}
		if errors.Is(err, context.Canceled) {
			return &Error{Kind: Unavailable, Msg: what + " was stopped", Err: err}
		}
		return &Error{Kind: Unavailable, Msg: "the mail server could not be reached; try again later", Err: err}
	}
	msg := e.Msg
	switch e.Status {
	case http.StatusBadRequest:
		return &Error{Kind: Invalid, Msg: msg, Err: err}
	case http.StatusNotFound:
		if msg == "" {
			msg = "not found"
		}
		return &Error{Kind: NotFound, Msg: msg, Err: err}
	case http.StatusConflict:
		return &Error{Kind: Conflict, Msg: msg, Err: err}
	case http.StatusRequestEntityTooLarge:
		return &Error{Kind: TooLarge, Msg: msg, Err: err}
	case http.StatusTooManyRequests:
		return &Error{Kind: Throttled, Msg: msg, Err: err}
	case http.StatusBadGateway:
		return &Error{Kind: Unavailable, Msg: msg, Err: err}
	case http.StatusInsufficientStorage:
		return &Error{Kind: NoSpace, Msg: msg, Err: err}
	}
	return &Error{Kind: Unavailable, Msg: "the mail server reported a problem; try again later", Err: err}
}

// importSource tidies what a person entered; the service checks it.
func importSource(src config.ImportSource) (config.ImportSource, error) {
	src.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(src.Host), "."))
	src.Username = strings.TrimSpace(src.Username)
	if src.Port == 0 {
		src.Port = 993
	}
	switch {
	case src.Host == "":
		return src, errorf(Invalid, "enter the name of the other mail server")
	case src.Port != 993 && src.Port != 143:
		return src, errorf(Invalid, "the other mail server must be reached on port 993 or 143")
	case src.Username == "":
		return src, errorf(Invalid, "enter the user name of the other mail account")
	case src.Password == "":
		return src, errorf(Invalid, "enter the password of the other mail account")
	}
	return src, nil
}

// CheckMailImport signs in to another mail account and shows its folders
// and sizes, without copying anything.
func (s *Service) CheckMailImport(ctx context.Context, a Actor, id int64, src config.ImportSource) (config.ImportCheck, error) {
	m, t, err := s.transferMailbox(ctx, a, id, true)
	if err != nil {
		return config.ImportCheck{}, err
	}
	if src, err = importSource(src); err != nil {
		return config.ImportCheck{}, err
	}
	if err := s.beginImport(a); err != nil {
		return config.ImportCheck{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, importCheckTimeout)
	defer cancel()
	c, err := t.CheckImport(ctx, m.StorageID, src)
	if err != nil {
		return config.ImportCheck{}, transferError(err, "checking the other account")
	}
	return c, nil
}

// StartMailImport starts copying from another mail account.
func (s *Service) StartMailImport(ctx context.Context, a Actor, id int64, start config.ImportStart) (MailImport, error) {
	m, t, err := s.transferMailbox(ctx, a, id, true)
	if err != nil {
		return MailImport{}, err
	}
	if start.Source, err = importSource(start.Source); err != nil {
		return MailImport{}, err
	}
	if err := s.beginImport(a); err != nil {
		return MailImport{}, err
	}
	ctx2, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	j, err := t.StartImport(ctx2, m.StorageID, start)
	if err != nil {
		return MailImport{}, transferError(err, "starting the import")
	}
	s.record(ctx, a, "mail.mailbox.import-started", m.LocalPart+"@"+m.Domain, importDetail(j, ""))
	return mailImportView(m, j), nil
}

// importDetail names a job's source for the activity log, never a password.
func importDetail(j config.ImportJob, note string) string {
	src := "file " + j.FileName
	if j.Kind == config.ImportIMAP {
		src = fmt.Sprintf("%s at %s", j.Username, j.Host)
	}
	d := fmt.Sprintf("import %d from %s", j.ID, src)
	if note != "" {
		d += " (" + note + ")"
	}
	return d
}

// CreateMailUpload declares a file to import; its parts follow with
// UploadMailPart.
func (s *Service) CreateMailUpload(ctx context.Context, a Actor, id int64, req config.UploadRequest) (MailImport, error) {
	m, t, err := s.transferMailbox(ctx, a, id, true)
	if err != nil {
		return MailImport{}, err
	}
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case req.Name == "" || len(req.Name) > 255:
		return MailImport{}, errorf(Invalid, "the file needs a name of at most 255 characters")
	case req.Size <= 0:
		return MailImport{}, errorf(Invalid, "the file is empty")
	case req.Size > config.MaxImportBytes:
		return MailImport{}, errorf(TooLarge, "a file to import may have at most %d GB", config.MaxImportBytes>>30)
	}
	if err := s.beginImport(a); err != nil {
		return MailImport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	j, err := t.CreateUpload(ctx, m.StorageID, req)
	if err != nil {
		return MailImport{}, transferError(err, "preparing the upload")
	}
	return mailImportView(m, j), nil
}

// UploadMailPart passes size bytes of an upload, starting at offset,
// through to the mail service without buffering. It returns how many
// bytes the service has now.
func (s *Service) UploadMailPart(ctx context.Context, a Actor, id, job, offset int64, part io.Reader, size int64) (int64, error) {
	m, t, err := s.transferMailbox(ctx, a, id, true)
	if err != nil {
		return 0, err
	}
	switch {
	case job <= 0:
		return 0, errorf(NotFound, "import not found")
	case offset < 0:
		return 0, errorf(Invalid, "the offset must not be negative")
	case size <= 0:
		return 0, errorf(Invalid, "the part is empty")
	case size > config.MaxUploadPart:
		return 0, errorf(TooLarge, "a part may have at most %d MB", config.MaxUploadPart>>20)
	}
	n, err := t.UploadPart(ctx, m.StorageID, job, offset, part, size)
	if err != nil {
		return 0, transferError(err, "the upload")
	}
	return n, nil
}

// ResumeMailImport starts an uploaded file's import once it was checked,
// or continues a paused or interrupted import from another account with
// its password.
func (s *Service) ResumeMailImport(ctx context.Context, a Actor, id, job int64, r config.ImportResume) (MailImport, error) {
	m, t, err := s.transferMailbox(ctx, a, id, true)
	if err != nil {
		return MailImport{}, err
	}
	if job <= 0 {
		return MailImport{}, errorf(NotFound, "import not found")
	}
	if err := s.beginImport(a); err != nil {
		return MailImport{}, err
	}
	ctx2, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	before, err := t.ImportJob(ctx2, m.StorageID, job)
	if err != nil {
		return MailImport{}, transferError(err, "starting the import")
	}
	if before.Kind == config.ImportIMAP && r.Password == "" {
		return MailImport{}, errorf(Invalid, "enter the password of the other mail account to continue")
	}
	j, err := t.ResumeImport(ctx2, m.StorageID, job, r)
	if err != nil {
		return MailImport{}, transferError(err, "starting the import")
	}
	note := ""
	if before.State == config.ImportPaused || before.State == config.ImportInterrupted {
		note = "continued"
	}
	s.record(ctx, a, "mail.mailbox.import-started", m.LocalPart+"@"+m.Domain, importDetail(j, note))
	return mailImportView(m, j), nil
}

// MailImports lists a mailbox's import jobs, newest first.
func (s *Service) MailImports(ctx context.Context, a Actor, id int64) ([]MailImport, error) {
	m, t, err := s.transferMailbox(ctx, a, id, false)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	js, err := t.ImportJobs(ctx, m.StorageID)
	if err != nil {
		return nil, transferError(err, "listing the imports")
	}
	out := make([]MailImport, 0, len(js))
	for _, j := range js {
		out = append(out, mailImportView(m, j))
	}
	return out, nil
}

// MailImportOf returns one import job with the messages it could not copy.
func (s *Service) MailImportOf(ctx context.Context, a Actor, id, job int64) (MailImport, error) {
	m, t, err := s.transferMailbox(ctx, a, id, false)
	if err != nil {
		return MailImport{}, err
	}
	if job <= 0 {
		return MailImport{}, errorf(NotFound, "import not found")
	}
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	j, err := t.ImportJob(ctx, m.StorageID, job)
	if err != nil {
		return MailImport{}, transferError(err, "reading the import")
	}
	return mailImportView(m, j), nil
}

// CancelMailImport stops an import that has not finished (what was copied
// stays), or removes a finished one from the list.
func (s *Service) CancelMailImport(ctx context.Context, a Actor, id, job int64) error {
	m, t, err := s.transferMailbox(ctx, a, id, true)
	if err != nil {
		return err
	}
	if job <= 0 {
		return errorf(NotFound, "import not found")
	}
	ctx2, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	j, err := t.ImportJob(ctx2, m.StorageID, job)
	if err != nil {
		return transferError(err, "cancelling the import")
	}
	if err := t.CancelImport(ctx2, m.StorageID, job); err != nil {
		return transferError(err, "cancelling the import")
	}
	if !config.ImportFinished(j.State) {
		s.record(ctx, a, "mail.mailbox.import-cancelled", m.LocalPart+"@"+m.Domain, importDetail(j, ""))
	}
	return nil
}

// MailExport is a running export: the zip stream and a file name for it.
type MailExport struct {
	Body     io.ReadCloser
	FileName string
}

// ExportMailbox streams all mail of a mailbox as a zip file. The caller
// must close Body; ctx ends the stream.
func (s *Service) ExportMailbox(ctx context.Context, a Actor, id int64) (MailExport, error) {
	m, t, err := s.transferMailbox(ctx, a, id, true)
	if err != nil {
		return MailExport{}, err
	}
	body, err := t.Export(ctx, m.StorageID)
	if err != nil {
		return MailExport{}, transferError(err, "the download")
	}
	addr := m.LocalPart + "@" + m.Domain
	s.record(ctx, a, "mail.mailbox.exported", addr, "")
	return MailExport{Body: body, FileName: exportFileName(addr, s.now())}, nil
}

// exportFileName is mail-<address>-<date>.zip with only characters that
// are safe in a file name everywhere.
func exportFileName(addr string, now time.Time) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == '@', r == '+':
			return r
		}
		return '_'
	}, addr)
	return "mail-" + safe + "-" + now.UTC().Format("2006-01-02") + ".zip"
}
