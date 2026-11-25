package transfer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

var (
	idAlice = mailtest.ID('a')
	idBob   = mailtest.ID('b')
)

const password = "s3cr3t-Pa55w0rd"

func init() { reconnectDelay = 10 * time.Millisecond }

// syncBuffer is a log sink that can be read while jobs write to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type env struct {
	t      *testing.T
	dir    string
	store  *storage.Store
	cfg    atomic.Pointer[directory.Directory]
	m      *Manager
	free   atomic.Int64
	logs   *syncBuffer
	imap   *fakeIMAP
	quota  int64
	mu     sync.Mutex
	events []config.Event
}

func newEnv(t *testing.T, mods ...func(*Options)) *env {
	t.Helper()
	e := &env{t: t, dir: t.TempDir(), logs: &syncBuffer{}}
	e.free.Store(100 << 30)
	st, err := storage.Open(e.dir, storage.Options{Quota: func(id string) int64 {
		if m, ok := e.cfg.Load().MailboxByStorageID(id); ok {
			return m.QuotaBytes
		}
		return 0
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.store = st
	e.setMailboxes(idAlice, idBob)
	e.imap = startFakeIMAP(t, "tls")
	e.m = e.manager(mods...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		e.m.Shutdown(ctx)
		st.Close()
		e.assertNoPassword()
		if t.Failed() {
			t.Logf("logs:\n%s", e.logs.String())
		}
	})
	return e
}

func (e *env) manager(mods ...func(*Options)) *Manager {
	opts := Options{
		Store:        e.store,
		Directory:    func() *directory.Directory { return e.cfg.Load() },
		FreeBytes:    func() (int64, error) { return e.free.Load(), nil },
		MinFreeBytes: 1 << 30,
		Event: func(ev config.Event) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
		Logger:       slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Resolver:     resolverFunc(func(ctx context.Context, h string) ([]net.IPAddr, error) { return e.imap.LookupIPAddr(ctx, h) }),
		Dial:         func(ctx context.Context, n, a string) (net.Conn, error) { return e.imap.dial(ctx, n, a) },
		RootCAs:      testRoots,
		AllowPrivate: true,
	}
	for _, f := range mods {
		f(&opts)
	}
	m := New(opts)
	if err := m.Start(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	return m
}

// setMailboxes configures the given mailboxes (alice@example.org, …).
func (e *env) setMailboxes(ids ...string) {
	cfg := &config.Config{Hostname: "mail.example.net", Domains: []config.Domain{{Name: "example.org"}},
		Limits: config.Limits{MaxMessageBytes: 100000}}
	for _, id := range ids {
		name := map[string]string{idAlice: "alice", idBob: "bob"}[id]
		cfg.Mailboxes = append(cfg.Mailboxes, config.Mailbox{StorageID: id, Address: name + "@example.org",
			PasswordHash: mailtest.Hash, QuotaBytes: e.quota})
	}
	e.cfg.Store(directory.New(cfg))
}

func (e *env) eventKinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		out = append(out, ev.Kind)
	}
	return out
}

// assertNoPassword checks logs, events and jobs for the password.
func (e *env) assertNoPassword() {
	e.t.Helper()
	if strings.Contains(e.logs.String(), password) {
		e.t.Errorf("password in the logs:\n%s", e.logs.String())
	}
	e.mu.Lock()
	evs, _ := json.Marshal(e.events)
	e.mu.Unlock()
	if bytes.Contains(evs, []byte(password)) {
		e.t.Errorf("password in events: %s", evs)
	}
}

func (e *env) jobJSON(sid string) string {
	js, err := e.store.Imports(context.Background(), sid)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := json.Marshal(js)
	return string(b)
}

// wait waits until a job is in one of the states.
func (e *env) wait(sid string, id int64, states ...string) config.ImportJob {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		j, err := e.m.Job(context.Background(), sid, id)
		if err == nil && slices.Contains(states, j.State) {
			return j
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("job %d: state %q (%s), want %v (%v)", id, j.State, j.Error, states, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) src() config.ImportSource {
	return config.ImportSource{Host: "imap.test", Port: 993, Username: "old@example.com", Password: password}
}

// messages returns folder → bodies of a mailbox.
func (e *env) messages(sid string) map[string][]string {
	e.t.Helper()
	ctx := context.Background()
	fs, err := e.store.ListFolders(ctx, sid)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string][]string{}
	for _, f := range fs {
		_, msgs, err := e.store.Select(ctx, sid, f.Name)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, m := range msgs {
			r, err := e.store.Open(m)
			if err != nil {
				e.t.Fatal(err)
			}
			b, _ := io.ReadAll(r)
			r.Close()
			out[f.Name] = append(out[f.Name], string(b))
		}
	}
	return out
}

func (e *env) message(sid, folder string, n int) storage.Message {
	e.t.Helper()
	_, msgs, err := e.store.Select(context.Background(), sid, folder)
	if err != nil || len(msgs) <= n {
		e.t.Fatalf("%s: %d messages (%v)", folder, len(msgs), err)
	}
	return msgs[n]
}

func status(err error) int {
	var te *Error
	if errors.As(err, &te) {
		return te.Status
	}
	return 0
}

// ---- fake IMAP server ----

type fakeMsg struct {
	uid   imap.UID
	flags []imap.Flag
	date  time.Time
	body  []byte
}

type fakeFolder struct {
	name        string
	attrs       []imap.MailboxAttr
	uidValidity uint32
	msgs        []*fakeMsg
	uidNext     imap.UID
}

// fakeIMAP serves folders over implicit TLS, like a provider would.
type fakeIMAP struct {
	t     *testing.T
	ln    net.Listener
	roots *x509.CertPool
	addr  string // what imap.test resolves to

	mu          sync.Mutex
	caps        imap.CapSet
	folders     []*fakeFolder
	writeSelect int // SELECT (not EXAMINE) commands
	bodyFetches int
	alertAfter  int // answer NO [ALERT] after this many body fetches (0: never)
	fetchDelay  time.Duration
	loginText   string // a failed sign-in answers this text
	logins      int
	nsPrefix    string
}

var (
	testCertOnce sync.Once
	testCert     tls.Certificate
	testRoots    *x509.CertPool
)

// startFakeIMAP starts a server with implicit TLS ("tls"), STARTTLS
// ("starttls") or neither ("plain").
func startFakeIMAP(t *testing.T, mode string) *fakeIMAP {
	testCertOnce.Do(func() {
		cert := mailtest.Cert(t, "imap.test", false)
		kp, err := tls.X509KeyPair([]byte(cert.CertPEM), []byte(cert.KeyPEM))
		if err != nil {
			panic(err)
		}
		testCert = kp
		testRoots = x509.NewCertPool()
		testRoots.AppendCertsFromPEM([]byte(cert.CertPEM))
	})
	f := &fakeIMAP{t: t, addr: "127.0.0.1", roots: testRoots, caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapNamespace: {}, imap.CapStatusSize: {}}}
	tlsConf := &tls.Config{Certificates: []tls.Certificate{testCert}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	opts := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &fakeSession{f: f}, nil, nil
		},
		Caps:   f.capsCopy(),
		Logger: discardLogger{},
	}
	var serveLn net.Listener = tls.NewListener(ln, tlsConf)
	switch mode {
	case "starttls":
		opts.TLSConfig, serveLn = tlsConf, ln
		opts.Caps[imap.CapStartTLS] = struct{}{}
	case "plain":
		opts.InsecureAuth, serveLn = true, ln
	}
	srv := imapserver.New(opts)
	go srv.Serve(serveLn)
	t.Cleanup(func() { srv.Close() })
	f.folder("INBOX")
	return f
}

type resolverFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

func (f resolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

func (f *fakeIMAP) capsCopy() imap.CapSet {
	c := imap.CapSet{}
	for k := range f.caps {
		c[k] = struct{}{}
	}
	return c
}

func (f *fakeIMAP) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if host != "imap.test" {
		return nil, errors.New("no such host")
	}
	return []net.IPAddr{{IP: net.ParseIP(f.addr)}}, nil
}

func (f *fakeIMAP) dial(ctx context.Context, network, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, f.ln.Addr().String())
}

func (f *fakeIMAP) folder(name string, attrs ...imap.MailboxAttr) *fakeFolder {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.folders {
		if x.name == name {
			return x
		}
	}
	x := &fakeFolder{name: name, attrs: attrs, uidValidity: uint32(len(f.folders) + 100), uidNext: 1}
	f.folders = append(f.folders, x)
	return x
}

func (f *fakeIMAP) add(folder, body string, flags ...imap.Flag) {
	x := f.folder(folder)
	f.mu.Lock()
	defer f.mu.Unlock()
	x.msgs = append(x.msgs, &fakeMsg{uid: x.uidNext, flags: flags, date: time.Date(2023, 5, int(x.uidNext), 8, 0, 0, 0, time.UTC), body: []byte(body)})
	x.uidNext++
}

func (f *fakeIMAP) flagsOf(folder string) [][]imap.Flag {
	x := f.folder(folder)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]imap.Flag
	for _, m := range x.msgs {
		out = append(out, slices.Clone(m.flags))
	}
	return out
}

func (f *fakeIMAP) counters() (writeSelect, bodyFetches int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeSelect, f.bodyFetches
}

func msg(subject string) string {
	return "From: a@example.com\r\nSubject: " + subject + "\r\nDate: Mon, 1 May 2023 08:00:00 +0000\r\n\r\nbody of " + subject + "\r\n"
}

type fakeSession struct {
	f   *fakeIMAP
	sel *fakeFolder
}

var errNotSupported = &imap.Error{Type: imap.StatusResponseTypeNo, Text: "not supported"}

func (s *fakeSession) Close() error { return nil }

func (s *fakeSession) Login(user, pass string) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.logins++
	if user != "old@example.com" || pass != password {
		text := s.f.loginText
		if text == "" {
			text = "Invalid credentials"
		}
		return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAuthenticationFailed, Text: text}
	}
	return nil
}

func (s *fakeSession) find(name string) (*fakeFolder, error) {
	for _, x := range s.f.folders {
		if x.name == name {
			return x, nil
		}
	}
	return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeNonExistent, Text: "no such folder"}
}

func (s *fakeSession) Select(name string, opts *imap.SelectOptions) (*imap.SelectData, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	x, err := s.find(name)
	if err != nil {
		return nil, err
	}
	if opts == nil || !opts.ReadOnly {
		s.f.writeSelect++
	}
	s.sel = x
	return &imap.SelectData{NumMessages: uint32(len(x.msgs)), UIDNext: x.uidNext, UIDValidity: x.uidValidity,
		Flags: []imap.Flag{imap.FlagSeen}}, nil
}

func (s *fakeSession) Create(string, *imap.CreateOptions) error         { return errNotSupported }
func (s *fakeSession) Delete(string) error                              { return errNotSupported }
func (s *fakeSession) Rename(string, string, *imap.RenameOptions) error { return errNotSupported }
func (s *fakeSession) Subscribe(string) error                           { return errNotSupported }
func (s *fakeSession) Unsubscribe(string) error                         { return errNotSupported }
func (s *fakeSession) Poll(*imapserver.UpdateWriter, bool) error        { return nil }
func (s *fakeSession) Unselect() error                                  { s.sel = nil; return nil }
func (s *fakeSession) Expunge(*imapserver.ExpungeWriter, *imap.UIDSet) error {
	return errNotSupported
}
func (s *fakeSession) Idle(*imapserver.UpdateWriter, <-chan struct{}) error { return errNotSupported }
func (s *fakeSession) Append(string, imap.LiteralReader, *imap.AppendOptions) (*imap.AppendData, error) {
	return nil, errNotSupported
}
func (s *fakeSession) Search(imapserver.NumKind, *imap.SearchCriteria, *imap.SearchOptions) (*imap.SearchData, error) {
	return nil, errNotSupported
}
func (s *fakeSession) Store(*imapserver.FetchWriter, imap.NumSet, *imap.StoreFlags, *imap.StoreOptions) error {
	return errNotSupported
}
func (s *fakeSession) Copy(imap.NumSet, string) (*imap.CopyData, error) { return nil, errNotSupported }

func (s *fakeSession) Namespace() (*imap.NamespaceData, error) {
	return &imap.NamespaceData{Personal: []imap.NamespaceDescriptor{{Prefix: s.f.nsPrefix, Delim: '/'}}}, nil
}

func (s *fakeSession) List(w *imapserver.ListWriter, _ string, patterns []string, _ *imap.ListOptions) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if len(patterns) == 0 {
		return w.WriteList(&imap.ListData{Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}, Delim: '/'})
	}
	for _, x := range s.f.folders {
		if err := w.WriteList(&imap.ListData{Mailbox: x.name, Delim: '/', Attrs: x.attrs}); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeSession) Status(name string, opts *imap.StatusOptions) (*imap.StatusData, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	x, err := s.find(name)
	if err != nil {
		return nil, err
	}
	n := uint32(len(x.msgs))
	var size int64
	for _, m := range x.msgs {
		size += int64(len(m.body))
	}
	d := &imap.StatusData{Mailbox: name, UIDNext: x.uidNext, UIDValidity: x.uidValidity}
	if opts.NumMessages {
		d.NumMessages = &n
	}
	if opts.Size {
		d.Size = &size
	}
	return d, nil
}

func inSet(set imap.NumSet, seq uint32, uid imap.UID, maxSeq uint32, maxUID imap.UID) bool {
	switch s := set.(type) {
	case imap.SeqSet:
		for _, r := range s {
			start, stop := r.Start, r.Stop
			if start == 0 {
				start = maxSeq
			}
			if stop == 0 {
				stop = maxSeq
			}
			if start > stop {
				start, stop = stop, start
			}
			if seq >= start && seq <= stop {
				return true
			}
		}
	case imap.UIDSet:
		for _, r := range s {
			start, stop := r.Start, r.Stop
			if start == 0 {
				start = maxUID
			}
			if stop == 0 {
				stop = maxUID
			}
			if start > stop {
				start, stop = stop, start
			}
			if uid >= start && uid <= stop {
				return true
			}
		}
	}
	return false
}

func (s *fakeSession) Fetch(w *imapserver.FetchWriter, set imap.NumSet, opts *imap.FetchOptions) error {
	s.f.mu.Lock()
	delay := s.f.fetchDelay
	if len(opts.BodySection) > 0 {
		s.f.bodyFetches++
		if s.f.alertAfter > 0 && s.f.bodyFetches > s.f.alertAfter {
			s.f.mu.Unlock()
			return &imap.Error{Type: imap.StatusResponseTypeNo, Code: imap.ResponseCodeAlert, Text: "Account exceeded command or bandwidth limits."}
		}
	}
	x := s.sel
	var maxUID imap.UID
	if n := len(x.msgs); n > 0 {
		maxUID = x.msgs[n-1].uid
	}
	type item struct {
		seq uint32
		m   fakeMsg
	}
	var items []item
	for i, m := range x.msgs {
		if !inSet(set, uint32(i+1), m.uid, uint32(len(x.msgs)), maxUID) {
			continue
		}
		for _, bs := range opts.BodySection {
			if !bs.Peek && !slices.Contains(m.flags, imap.FlagSeen) {
				m.flags = append(m.flags, imap.FlagSeen)
			}
		}
		items = append(items, item{uint32(i + 1), fakeMsg{uid: m.uid, flags: slices.Clone(m.flags), date: m.date, body: m.body}})
	}
	s.f.mu.Unlock()
	if delay > 0 && len(opts.BodySection) > 0 {
		time.Sleep(delay)
	}
	for _, it := range items {
		rw := w.CreateMessage(it.seq)
		rw.WriteUID(it.m.uid)
		if opts.Flags {
			rw.WriteFlags(it.m.flags)
		}
		if opts.InternalDate {
			rw.WriteInternalDate(it.m.date)
		}
		if opts.RFC822Size {
			rw.WriteRFC822Size(int64(len(it.m.body)))
		}
		for _, bs := range opts.BodySection {
			wc := rw.WriteBodySection(bs, int64(len(it.m.body)))
			wc.Write(it.m.body)
			if err := wc.Close(); err != nil {
				return err
			}
		}
		if err := rw.Close(); err != nil {
			return err
		}
	}
	return nil
}

var _ imapserver.SessionNamespace = (*fakeSession)(nil)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }
