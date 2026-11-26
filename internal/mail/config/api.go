package config

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Admin socket endpoints (see Client for their use):
//
//	POST   /load                            body Config; 204, or 400 with an error text
//	GET    /status                          Status
//	GET    /events?after=<id>               []Event, oldest first, at most 500
//	POST   /mailboxes/{storage_id}/unblock  204, 404 if unknown
//	DELETE /mailboxes/{storage_id}          204 (also when there was no data), 409 while configured
//	POST   /checks/outbound                 OutboundCheck
//	POST   /send                            body OutgoingMail; 204 once the message is stored or queued, 400 with an error text
//
// Import and export of a mailbox (the mailbox must be configured, else
// 404; see "Import and export" in wiki/E-Mail-Architecture.md):
//
//	POST   /mailboxes/{sid}/imports/check         body ImportSource → ImportCheck
//	POST   /mailboxes/{sid}/imports               body ImportStart → ImportJob (waiting)
//	POST   /mailboxes/{sid}/uploads               body UploadRequest → ImportJob (uploading)
//	PUT    /mailboxes/{sid}/uploads/{id}?offset=N body: the bytes → UploadProgress
//	POST   /mailboxes/{sid}/imports/{id}/start    body ImportResume → ImportJob (waiting)
//	GET    /mailboxes/{sid}/imports               []ImportJob, newest first, without failures
//	GET    /mailboxes/{sid}/imports/{id}          ImportJob with Failures
//	DELETE /mailboxes/{sid}/imports/{id}          204: cancels an unfinished job, removes a finished one
//	GET    /mailboxes/{sid}/export                application/zip, streamed
//
// Answers that are not a success carry a plain-text reason meant for
// people (never a password): 400 invalid input, 404 unknown mailbox or job,
// 409 not possible in the job's state, another import of the mailbox not
// finished yet, or an export of it running, 413 over MaxImportBytes or
// MaxUploadPart, 429 more than ImportRateLimit checks and starts per
// mailbox and hour or more than MaxExports exports at once, 502 the other
// server failed (connection, certificate, sign-in, its answer), 507 not
// enough disk space.
//
// DELETE /mailboxes/{sid} cancels the mailbox's import jobs and waits for
// them before deleting its data.
const (
	PathLoad     = "/load"
	PathStatus   = "/status"
	PathEvents   = "/events"
	PathMailbox  = "/mailboxes/"
	PathOutbound = "/checks/outbound"
	PathSend     = "/send"

	// Below PathMailbox + storage id.
	PathImports      = "/imports"
	PathImportsCheck = "/imports/check"
	PathUploads      = "/uploads"
	PathExport       = "/export"
)

// Limits of import and export (the service enforces them; the panel may
// check them earlier for a better message).
const (
	// MaxImportBytes is the largest file that can be uploaded for import.
	MaxImportBytes = 20 << 30
	// MaxUploadPart is the largest part of an upload sent in one PUT.
	MaxUploadPart = 256 << 20
	// ImportRateLimit is how many checks and starts (also uploads and
	// continuing a job) a mailbox may have per hour.
	ImportRateLimit = 10
	// MaxImportFailures is how many failed or skipped messages a job lists.
	MaxImportFailures = 1000
	// ImportRetention is how long finished jobs are kept.
	ImportRetention = 30 * 24 * time.Hour
	// MaxExports is how many exports run at once in the service (one per
	// mailbox).
	MaxExports = 4
	// StreamIdleTimeout ends an upload or export on the admin socket when no
	// byte moved for this long.
	StreamIdleTimeout = 2 * time.Minute
)

// Import job kinds.
const (
	ImportIMAP = "imap"
	ImportFile = "file"
)

// Import job states. A job goes uploading → checking → ready (file) or
// starts waiting (IMAP), then waiting → running → done, failed or
// cancelled. A running IMAP job becomes paused when the other server
// limits downloads (Gmail) and interrupted when the service restarted;
// both continue with ImportResume and the password. A file job continues
// by itself after a restart.
const (
	ImportUploading   = "uploading"
	ImportChecking    = "checking"
	ImportReady       = "ready"
	ImportWaiting     = "waiting"
	ImportRunning     = "running"
	ImportPaused      = "paused"
	ImportDone        = "done"
	ImportFailed      = "failed"
	ImportCancelled   = "cancelled"
	ImportInterrupted = "interrupted"
)

// ImportFinished tells whether a job in state s has ended for good.
func ImportFinished(s string) bool {
	return s == ImportDone || s == ImportFailed || s == ImportCancelled
}

// ImportSource is another mail account to import from over IMAP. It is
// only ever sent to the service, never answered: the password is used
// for this check or job and kept in memory only. String and LogValue hide
// the password, so the value can be logged by mistake without harm.
type ImportSource struct {
	// Host is a host name or IP address.
	Host string `json:"host"`
	// Port is 993 (TLS) or 143 (STARTTLS required).
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s ImportSource) String() string {
	return fmt.Sprintf("%s@%s:%d", s.Username, s.Host, s.Port)
}

// GoString keeps %#v from printing the password.
func (s ImportSource) GoString() string { return "config.ImportSource{" + s.String() + "}" }

// LogValue keeps log/slog from printing the password.
func (s ImportSource) LogValue() slog.Value {
	return slog.GroupValue(slog.String("host", s.Host), slog.Int("port", s.Port), slog.String("username", s.Username))
}

// ImportChoices say what a job copies.
type ImportChoices struct {
	// Folders are the source folders to copy (ImportFolder.Remote). Nil
	// means those selected by default (ImportFolder.Selected), plus junk
	// and trash folders as IncludeJunk and IncludeTrash say. When set,
	// exactly these folders are copied and the two flags are ignored;
	// unknown names are refused (400).
	Folders      []string `json:"folders,omitempty"`
	IncludeJunk  bool     `json:"include_junk,omitempty"`
	IncludeTrash bool     `json:"include_trash,omitempty"`
}

// ImportStart starts an IMAP import.
type ImportStart struct {
	Source  ImportSource  `json:"source"`
	Choices ImportChoices `json:"choices"`
}

// ImportResume starts a file job that is ready (Choices used, Password
// ignored) or continues a paused or interrupted IMAP job (Password
// required, Choices ignored: the job keeps its own).
type ImportResume struct {
	Choices  ImportChoices `json:"choices"`
	Password string        `json:"password,omitempty"`
}

func (r ImportResume) String() string { return fmt.Sprintf("{%+v}", r.Choices) }

// GoString keeps %#v from printing the password.
func (r ImportResume) GoString() string { return "config.ImportResume" + r.String() }

// LogValue keeps log/slog from printing the password.
func (r ImportResume) LogValue() slog.Value { return slog.StringValue(r.String()) }

// UploadRequest declares a file upload for import.
type UploadRequest struct {
	// Name is the file name as the person chose it (shown, and used to
	// recognise Google Takeout); at most 255 bytes.
	Name string `json:"name"`
	// Size is the exact size in bytes, at most MaxImportBytes.
	Size int64 `json:"size"`
}

// UploadProgress is how many bytes of an upload the service has stored.
type UploadProgress struct {
	Received int64 `json:"received"`
}

// Notes on a source folder (ImportFolder.Note).
const (
	ImportNoteJunk      = "junk"      // not selected by default
	ImportNoteTrash     = "trash"     // not selected by default
	ImportNoteStarred   = "starred"   // Gmail: repeats other folders, not selected
	ImportNoteImportant = "important" // Gmail: repeats other folders, not selected
	ImportNoteAllMail   = "all-mail"  // Gmail: copied last into Archive, without what is there already
	ImportNoteTooMany   = "too-many"  // over the limit of folders or depth: not copied
)

// ImportFolder is one source folder in an ImportCheck.
type ImportFolder struct {
	// Remote is the folder name at the source (the name in the file for
	// file imports), as ImportChoices.Folders refers to it.
	Remote string `json:"remote"`
	// Local is the folder it is copied into here; existing folders are
	// merged into.
	Local string `json:"local"`
	// SpecialUse is the special use of the local folder (e.g. "\Sent"),
	// or "".
	SpecialUse string `json:"special_use,omitempty"`
	Messages   int    `json:"messages"`
	Bytes      int64  `json:"bytes"`
	// Selected tells whether the folder is copied by default.
	Selected bool   `json:"selected"`
	Note     string `json:"note,omitempty"`
}

// ImportCheck is what a source holds and whether it fits.
type ImportCheck struct {
	Folders []ImportFolder `json:"folders"`
	// Messages and Bytes count the folders selected by default.
	Messages int   `json:"messages"`
	Bytes    int64 `json:"bytes"`
	// QuotaBytes is the mailbox's quota (0: no limit), UsedBytes what it
	// stores, QuotaLeft what is left (-1: no limit).
	QuotaBytes int64 `json:"quota_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
	QuotaLeft  int64 `json:"quota_left"`
	// SkippedEntries counts entries of an uploaded file that are not mail
	// or were refused (unsafe names, nested archives, too large).
	SkippedEntries int `json:"skipped_entries,omitempty"`
	// OwnExport is set when an uploaded file is a "Download all mail" zip
	// of this panel: it was cleaned up once already, so the interface
	// suggests including Junk and Trash.
	OwnExport bool `json:"own_export,omitempty"`
}

// ImportFolderProgress is the mapping of one source folder and what was
// copied from it.
type ImportFolderProgress struct {
	Remote  string `json:"remote"`
	Local   string `json:"local"`
	Copied  int    `json:"copied"`
	Skipped int    `json:"skipped"`
	Failed  int    `json:"failed"`
	// Done is set once the folder was read to the end.
	Done bool `json:"done,omitempty"`
}

// ImportFailure is a message that was not copied.
type ImportFailure struct {
	Folder  string    `json:"folder"`
	Date    time.Time `json:"date,omitzero"`
	Subject string    `json:"subject,omitempty"` // shortened
	Reason  string    `json:"reason"`
}

// ImportJob is an import into a mailbox. It never holds a password.
type ImportJob struct {
	ID        int64  `json:"id"`
	StorageID string `json:"storage_id"`
	Kind      string `json:"kind"` // ImportIMAP or ImportFile
	State     string `json:"state"`
	// Error says why a job failed or paused (plain text, one line).
	Error string `json:"error,omitempty"`

	// IMAP source.
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`

	// File source: name, declared size and bytes received so far.
	FileName string `json:"file_name,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	Received int64  `json:"received,omitempty"`
	// Check is the overview of a file once it was scanned.
	Check *ImportCheck `json:"check,omitempty"`

	Choices ImportChoices `json:"choices"`
	// Folders maps the source folders being copied to ours, in the order
	// they are copied, with progress.
	Folders []ImportFolderProgress `json:"folders"`
	// Messages is how many messages the job will look at (known once it
	// runs); Copied, Skipped (already there) and Failed how it went;
	// Bytes is what was copied.
	Messages int   `json:"messages"`
	Copied   int   `json:"copied"`
	Skipped  int   `json:"skipped"`
	Failed   int   `json:"failed"`
	Bytes    int64 `json:"bytes"`

	Created  time.Time `json:"created"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`

	// Failures lists messages not copied (at most MaxImportFailures);
	// only GET /mailboxes/{sid}/imports/{id} fills it.
	Failures []ImportFailure `json:"failures,omitempty"`

	// Position is internal to the service (how far a file job got).
	Position int64 `json:"-"`
	// Scans is internal to the service: how often the scan of an upload
	// started without coming to an end.
	Scans int `json:"-"`
}

// OutgoingMail is a message the panel itself sends (password reset links
// and the like). The service writes the message, signs it for the domain
// of From and delivers or queues it. From must be a plain address in a
// configured domain; bounces are dropped, so it is typically a no-reply
// address. Text is required; HTML, when set, is sent as an alternative.
type OutgoingMail struct {
	From     string `json:"from"`
	FromName string `json:"from_name,omitempty"`
	To       string `json:"to"`
	Subject  string `json:"subject"`
	Text     string `json:"text"`
	HTML     string `json:"html,omitempty"`
}

// Status is what the service reports about itself.
type Status struct {
	// ConfigHash is Config.Hash of the loaded configuration ("" if none).
	ConfigHash string `json:"config_hash"`
	// Listeners tells per listener ("smtp", "submission", "submissions",
	// "imaps") whether it is listening, and the error if it could not.
	Listeners []Listener `json:"listeners"`
	// CertificateExpires is when the first certificate expires (zero if none).
	CertificateExpires time.Time `json:"certificate_expires"`
	// QueueLength is the number of messages waiting to be delivered.
	QueueLength int `json:"queue_length"`
	// Mailboxes reports usage and block state of every configured mailbox.
	Mailboxes []MailboxStatus `json:"mailboxes"`
	// Orphans are storage ids that have data but are not configured.
	Orphans []string `json:"orphans"`
}

// Listener is the state of one port.
type Listener struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
}

// MailboxStatus is what the service knows about one mailbox.
type MailboxStatus struct {
	StorageID string `json:"storage_id"`
	UsedBytes int64  `json:"used_bytes"`
	Messages  int    `json:"messages"`
	// Blocked is set when the service stopped the mailbox from sending.
	Blocked       bool      `json:"blocked"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
	BlockedAt     time.Time `json:"blocked_at,omitzero"`
}

// Event kinds.
const (
	EventBlocked        = "mailbox.blocked"
	EventSignInFailures = "mailbox.sign-in-failures"
	EventBounced        = "delivery.bounced"
	// EventSendLimit: a mailbox or domain reached a sending limit (at most
	// one event per mailbox and kind of limit per hour).
	EventSendLimit      = "mailbox.send-limit"
	EventListenerFailed = "listener.failed"
	// EventBlocklistRefused: a DNS blocklist refused to answer this
	// server's queries (answers in 127.255.255.0/24, typical through public
	// resolvers); at most one event per blocklist and day. Mail health
	// shows it.
	EventBlocklistRefused = "blocklist.refused"
	// EventNotForwarded: a message was delivered (or accepted) but not
	// forwarded to an outside address: it was judged Junk, or the domain
	// reached its sending limit.
	EventNotForwarded = "delivery.not-forwarded"
	// Import jobs that ended or stopped (StorageID set; Detail names the
	// job id, the source host and user or file name, and the counts, never
	// the password).
	EventImportFinished    = "import.finished"
	EventImportFailed      = "import.failed"
	EventImportPaused      = "import.paused"
	EventImportInterrupted = "import.interrupted"
)

// Event is something the panel should record in its activity log. Detail is
// plain text without secrets.
type Event struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	StorageID string    `json:"storage_id,omitempty"`
	Address   string    `json:"address,omitempty"`
	Detail    string    `json:"detail"`
}

// OutboundCheck is the result of trying to reach port 25 of another server.
type OutboundCheck struct {
	OK     bool   `json:"ok"`
	Target string `json:"target"`
	Error  string `json:"error,omitempty"`
}

// ParseDKIMKey parses a PEM PKCS#8 (or PKCS#1) RSA private key of at least
// 1024 bits.
func ParseDKIMKey(p string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unexpected PEM type %q", block.Type)
	}
	if err != nil {
		return nil, err
	}
	k, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	if k.N.BitLen() < 1024 {
		return nil, errors.New("RSA key too short")
	}
	return k, nil
}

// Client talks to the mail service's admin socket.
type Client struct {
	http *http.Client
	// stream carries uploads, exports and IMAP checks: no overall
	// timeout (they may take long), the request's context ends them, and
	// a connection on which no byte moved for StreamIdleTimeout fails.
	stream *http.Client
}

// NewClient returns a client for the Unix socket at path.
func NewClient(path string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
	return &Client{
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{Proxy: nil, DialContext: dial},
		},
		stream: &http.Client{
			Transport: &http.Transport{
				Proxy: nil,
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					c, err := dial(ctx, network, addr)
					if err != nil {
						return nil, err
					}
					return &IdleConn{Conn: c, Timeout: StreamIdleTimeout}, nil
				},
				// Streams are rare; idle connections are not kept.
				DisableKeepAlives: true,
			},
		},
	}
}

// IdleConn is a connection whose deadline moves forward before every
// read and write: it fails only when no byte moved for Timeout.
type IdleConn struct {
	net.Conn
	Timeout time.Duration
}

func (c *IdleConn) Read(b []byte) (int, error) {
	c.Conn.SetReadDeadline(time.Now().Add(c.Timeout))
	return c.Conn.Read(b)
}

func (c *IdleConn) Write(b []byte) (int, error) {
	c.Conn.SetWriteDeadline(time.Now().Add(c.Timeout))
	return c.Conn.Write(b)
}

// base is ignored for Unix sockets except as the Host header.
const base = "http://ownpanel-mail"

// Error is an answer of the service that is not a success.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("mail service: %s (%d)", e.Msg, e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	return c.doWith(ctx, c.http, method, path, body, out)
}

func (c *Client) doWith(ctx context.Context, hc *http.Client, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeAnswer(resp, out)
}

func decodeAnswer(resp *http.Response, out any) error {
	if resp.StatusCode/100 != 2 {
		return answerError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

func answerError(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &Error{Status: resp.StatusCode, Msg: string(bytes.TrimSpace(msg))}
}

// Load replaces the service's configuration.
func (c *Client) Load(ctx context.Context, cfg *Config) error {
	return c.do(ctx, http.MethodPost, PathLoad, cfg, nil)
}

// Status asks the service how it is.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.do(ctx, http.MethodGet, PathStatus, nil, &s)
	return s, err
}

// Events returns events with an id greater than after.
func (c *Client) Events(ctx context.Context, after int64) ([]Event, error) {
	var ev []Event
	err := c.do(ctx, http.MethodGet, PathEvents+"?after="+strconv.FormatInt(after, 10), nil, &ev)
	return ev, err
}

// Unblock lets a blocked mailbox send again.
func (c *Client) Unblock(ctx context.Context, storageID string) error {
	if !ValidStorageID(storageID) {
		return errors.New("invalid storage id")
	}
	return c.do(ctx, http.MethodPost, PathMailbox+url.PathEscape(storageID)+"/unblock", nil, nil)
}

// DeleteMailbox removes all data of a mailbox that is no longer in the
// loaded configuration (409 otherwise).
func (c *Client) DeleteMailbox(ctx context.Context, storageID string) error {
	if !ValidStorageID(storageID) {
		return errors.New("invalid storage id")
	}
	return c.do(ctx, http.MethodDelete, PathMailbox+url.PathEscape(storageID), nil, nil)
}

// Send hands a message to the service. An *Error with status 400 carries
// the reason the service refused it (for example an unknown recipient).
func (c *Client) Send(ctx context.Context, m OutgoingMail) error {
	return c.do(ctx, http.MethodPost, PathSend, m, nil)
}

// CheckOutbound asks the service to try a connection to port 25 elsewhere.
func (c *Client) CheckOutbound(ctx context.Context) (OutboundCheck, error) {
	var r OutboundCheck
	err := c.do(ctx, http.MethodPost, PathOutbound, nil, &r)
	return r, err
}

func mailboxPath(storageID string) (string, error) {
	if !ValidStorageID(storageID) {
		return "", errors.New("invalid storage id")
	}
	return PathMailbox + url.PathEscape(storageID), nil
}

func jobPath(storageID string, id int64, under string) (string, error) {
	p, err := mailboxPath(storageID)
	if err != nil {
		return "", err
	}
	if id <= 0 {
		return "", errors.New("invalid job id")
	}
	return p + under + "/" + strconv.FormatInt(id, 10), nil
}

// CheckImport signs in to another account and reports its folders and
// sizes without copying anything. It may take a while (one STATUS or
// FETCH per folder), so it has no overall timeout: bound it with ctx.
func (c *Client) CheckImport(ctx context.Context, storageID string, src ImportSource) (ImportCheck, error) {
	var r ImportCheck
	p, err := mailboxPath(storageID)
	if err != nil {
		return r, err
	}
	err = c.doWith(ctx, c.stream, http.MethodPost, p+PathImportsCheck, src, &r)
	return r, err
}

// StartImport starts an IMAP import; the job waits until it may run.
func (c *Client) StartImport(ctx context.Context, storageID string, start ImportStart) (ImportJob, error) {
	var j ImportJob
	p, err := mailboxPath(storageID)
	if err != nil {
		return j, err
	}
	err = c.do(ctx, http.MethodPost, p+PathImports, start, &j)
	return j, err
}

// CreateUpload declares a file to import and returns its job (state
// uploading); send the bytes with UploadPart.
func (c *Client) CreateUpload(ctx context.Context, storageID string, req UploadRequest) (ImportJob, error) {
	var j ImportJob
	p, err := mailboxPath(storageID)
	if err != nil {
		return j, err
	}
	err = c.do(ctx, http.MethodPost, p+PathUploads, req, &j)
	return j, err
}

// UploadPart sends size bytes (at most MaxUploadPart) of an upload that
// start at offset, which must be what the service has received so far
// (ImportJob.Received; 409 otherwise, then ask ImportJob and go on from
// there). It returns how many bytes the service has now. After the last
// part the job moves on to checking by itself. Not bounded by the 30 s
// timeout: ctx and an idle timeout end it.
func (c *Client) UploadPart(ctx context.Context, storageID string, id, offset int64, part io.Reader, size int64) (int64, error) {
	p, err := jobPath(storageID, id, PathUploads)
	if err != nil {
		return 0, err
	}
	if size <= 0 || size > MaxUploadPart || offset < 0 {
		return 0, errors.New("invalid upload part")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+p+"?offset="+strconv.FormatInt(offset, 10), io.LimitReader(part, size))
	if err != nil {
		return 0, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.stream.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var pr UploadProgress
	err = decodeAnswer(resp, &pr)
	return pr.Received, err
}

// ResumeImport starts a file job that is ready, or continues a paused or
// interrupted IMAP job with the password.
func (c *Client) ResumeImport(ctx context.Context, storageID string, id int64, r ImportResume) (ImportJob, error) {
	var j ImportJob
	p, err := jobPath(storageID, id, PathImports)
	if err != nil {
		return j, err
	}
	err = c.do(ctx, http.MethodPost, p+"/start", r, &j)
	return j, err
}

// ImportJobs lists a mailbox's import jobs, newest first (without
// failures).
func (c *Client) ImportJobs(ctx context.Context, storageID string) ([]ImportJob, error) {
	var js []ImportJob
	p, err := mailboxPath(storageID)
	if err != nil {
		return nil, err
	}
	err = c.do(ctx, http.MethodGet, p+PathImports, nil, &js)
	return js, err
}

// ImportJob returns one job with its failures.
func (c *Client) ImportJob(ctx context.Context, storageID string, id int64) (ImportJob, error) {
	var j ImportJob
	p, err := jobPath(storageID, id, PathImports)
	if err != nil {
		return j, err
	}
	err = c.do(ctx, http.MethodGet, p, nil, &j)
	return j, err
}

// CancelImport cancels a job that has not finished (it ends cancelled;
// what was copied stays) and waits for it to stop, or removes a finished
// job from the list.
func (c *Client) CancelImport(ctx context.Context, storageID string, id int64) error {
	p, err := jobPath(storageID, id, PathImports)
	if err != nil {
		return err
	}
	return c.doWith(ctx, c.stream, http.MethodDelete, p, nil, nil)
}

// Export streams all mail of a mailbox as a zip file (see "Export" in
// the architecture). The caller must close the reader; ctx and an idle
// timeout end the stream. An error after the stream started shows as a
// read error (the zip is then incomplete).
func (c *Client) Export(ctx context.Context, storageID string) (io.ReadCloser, error) {
	p, err := mailboxPath(storageID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+p+PathExport, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, answerError(resp)
	}
	return resp.Body, nil
}
