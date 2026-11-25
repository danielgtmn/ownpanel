// Package server is the mail service process (ownpaneld mail): the admin
// socket the panel drives it through, the configuration it keeps across
// restarts, and the network listeners.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/filter"
	"github.com/danielgtmn/ownpanel/internal/mail/imapd"
	"github.com/danielgtmn/ownpanel/internal/mail/queue"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/smtpd"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
	"github.com/danielgtmn/ownpanel/internal/mail/submission"
	"github.com/danielgtmn/ownpanel/internal/mail/transfer"
)

// Options configure the service.
type Options struct {
	DataDir     string
	AdminSocket string
	// WebSocket is the Unix socket Caddy passes the mail host's
	// self-service API and calendars and contacts to (package web); ""
	// leaves it closed.
	WebSocket string
	// WebGroup, when set, is the group the web socket's directory and the
	// socket are handed to (the web server's; the service must be a
	// member).
	WebGroup string
	// AccountHandler serves /account/api/… on the web socket (see
	// web.Options.Account).
	AccountHandler http.Handler
	// DAVHandler serves calendars and contacts on the web socket (see
	// web.Options.DAV).
	DAVHandler http.Handler
	// Listener addresses: SMTP (port 25), submission with STARTTLS (587),
	// submission with TLS (465) and IMAPS (993). An empty address leaves
	// that listener closed.
	SMTPAddr        string
	SubmissionAddr  string
	SubmissionsAddr string
	IMAPSAddr       string
	Logger          *slog.Logger

	// FreeBytes reports free disk space (tests); nil measures the data
	// directory.
	FreeBytes func() (int64, error)
	// Outbound runs the outbound port 25 check (tests); nil tries the MX
	// of gmail.com.
	Outbound func(ctx context.Context) config.OutboundCheck

	// IPv6Outbound lets the queue deliver over IPv6 (see package queue).
	IPv6Outbound bool
	// QueueResolver, QueueDial and QueuePort replace MX lookups, outgoing
	// connections and port 25 of the queue (tests).
	QueueResolver queue.Resolver
	QueueDial     func(ctx context.Context, network, address string) (net.Conn, error)
	QueuePort     string
	// AllowPrivateMX lets the queue deliver to mail servers on loopback,
	// private and other non-public addresses (tests and test networks).
	AllowPrivateMX bool
	// Resolver answers the DNS lookups of the checks of incoming mail
	// (SPF, DKIM, DMARC, blocklists, reverse DNS); nil means the system
	// resolver (tests replace it).
	Resolver auth.Resolver

	// ImportResolver, ImportDial and ImportRootCAs replace address lookups,
	// connections and trusted certificates of IMAP imports (tests); nil
	// means the system's. AllowPrivateMX also lets imports reach private
	// addresses.
	ImportResolver transfer.Resolver
	ImportDial     func(ctx context.Context, network, address string) (net.Conn, error)
	ImportRootCAs  *x509.CertPool
}

// configFile is the last configuration loaded, used at start like Caddy's
// --resume.
const configFile = "config.json"

// maxConfigBytes bounds a configuration upload.
const maxConfigBytes = 32 << 20

// shutdownGrace bounds how long running transactions may take on stop.
const shutdownGrace = 30 * time.Second

// Server is the running mail service.
type Server struct {
	opts  Options
	log   *slog.Logger
	store *storage.Store

	state atomic.Pointer[runtime]
	tls   *tls.Config

	ignoredMu sync.Mutex
	ignored   string // grants last logged as ignored

	loadMu sync.Mutex // serializes loads and account changes
	// overlays are the account changes mailboxes made here that the
	// pushed configuration does not include yet, by storage id (see
	// directory.Overlay). Guarded by loadMu; also kept in mail.db.
	overlays map[string]*directory.Overlay

	signIn  *signin.Checker
	limits  *submission.Limiter
	signer  *auth.Signer
	queue   *queue.Queue
	imports *transfer.Manager
	filter  *filter.Filter
	srs     *auth.SRS

	lisMu       sync.Mutex
	smtp        listenerState
	submission  listenerState
	submissions listenerState
	imaps       listenerState

	admin   *http.Server
	adminLn net.Listener
	web     *http.Server // nil without Options.WebSocket or when it failed
	webLn   net.Listener
	webErr  string // why the web socket does not work; guarded by lisMu
}

// runtime is an immutable snapshot of the loaded configuration.
type runtime struct {
	// pushed is the configuration as the panel pushed it; dir is built
	// from it with the account overlays applied.
	pushed *config.Config
	dir    *directory.Directory
	hash   string // of pushed; "" when nothing was loaded
	certs  *certSet
}

// service is a protocol server behind one listener.
type service interface {
	Serve(net.Listener) error
	Reconfigure()
	Shutdown(context.Context) error
}

type listenerState struct {
	name  string // as in config.Listener
	label string // for events: what the port is for
	cfg   string // configured address
	start func() service

	srv       service
	addr      string
	err       string
	reportErr string // last error recorded as an event
}

// New opens the data directory, checks the storage against its files and
// loads the last configuration, if any. It does not open ports yet.
func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{opts: opts, log: opts.Logger}
	s.smtp = listenerState{name: "smtp", label: "incoming mail", cfg: opts.SMTPAddr, start: s.startSMTP}
	s.submission = listenerState{name: "submission", label: "sending for mail clients (STARTTLS)", cfg: opts.SubmissionAddr, start: func() service { return s.startSubmission(false) }}
	s.submissions = listenerState{name: "submissions", label: "sending for mail clients (TLS)", cfg: opts.SubmissionsAddr, start: func() service { return s.startSubmission(true) }}
	s.imaps = listenerState{name: "imaps", label: "IMAP for mail clients", cfg: opts.IMAPSAddr, start: s.startIMAPS}
	s.signIn = signin.New(signin.Options{Event: s.event})
	empty := &config.Config{}
	s.state.Store(&runtime{pushed: empty, dir: directory.New(empty), certs: &certSet{}})
	s.tls = &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return s.state.Load().certs.pick(hello)
		},
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}
	store, err := storage.Open(opts.DataDir, storage.Options{Quota: s.quota})
	if err != nil {
		return nil, err
	}
	s.store = store
	rep, err := store.Reconcile(context.Background())
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("check storage: %w", err)
	}
	if rep != (storage.ReconcileReport{}) {
		s.log.Warn("mail storage repaired", "missing_files", rep.MissingFiles, "indexed_files", rep.IndexedFiles,
			"restored", rep.Restored, "new_uidvalidity", rep.NewUIDValidity,
			"queue_missing_files", rep.QueueMissing, "queue_stray_files", rep.QueueStray)
	}
	s.limits = submission.NewLimiter(store, nil, s.event, s.log)
	s.signer = auth.NewSigner()
	srsKey, err := auth.LoadSRSKey(opts.DataDir)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("SRS key: %w", err)
	}
	s.srs = auth.NewSRS(srsKey, nil)
	s.filter = filter.New(filter.Options{Resolver: opts.Resolver, Store: store, Event: s.event, Logger: s.log})
	s.queue = queue.New(queue.Options{
		ReturnTo:     s.returnTo,
		Store:        store,
		Directory:    s.Directory,
		Resolver:     opts.QueueResolver,
		Dial:         opts.QueueDial,
		Port:         opts.QueuePort,
		IPv6:         opts.IPv6Outbound,
		AllowPrivate: opts.AllowPrivateMX,
		Event:        s.event,
		Logger:       s.log,
	})
	imp := transfer.Options{
		Store:        store,
		Directory:    s.Directory,
		FreeBytes:    opts.FreeBytes,
		MinFreeBytes: smtpd.DefaultMinFreeBytes,
		Event:        s.event,
		Logger:       s.log,
		Dial:         opts.ImportDial,
		RootCAs:      opts.ImportRootCAs,
		AllowPrivate: opts.AllowPrivateMX,
	}
	if opts.ImportResolver != nil {
		imp.Resolver = opts.ImportResolver
	}
	s.imports = transfer.New(imp)
	if s.overlays, err = loadOverlays(store); err != nil {
		store.Close()
		return nil, fmt.Errorf("account changes: %w", err)
	}
	if err := s.resume(); err != nil {
		// A broken file must not keep mail down: the panel pushes again.
		s.log.Error("mail: last configuration not loaded", "err", err)
	}
	return s, nil
}

// returnTo maps an SRS address of ours to the original sender, for
// bounces of forwarded mail.
func (s *Server) returnTo(sender string) (string, bool) {
	local, dom, ok := config.SplitAddress(directory.Normalize(sender))
	if !ok || !auth.IsSRS(local) {
		return "", false
	}
	dir := s.Directory()
	if _, ok := dir.Domain(dom); !ok && dom != dir.Hostname() {
		return "", false
	}
	orig, err := s.srs.Reverse(sender)
	if err != nil {
		return "", false
	}
	return orig, true
}

func (s *Server) quota(storageID string) int64 {
	if m, ok := s.state.Load().dir.MailboxByStorageID(storageID); ok {
		return m.QuotaBytes
	}
	return 0
}

// Directory is the current configuration view.
func (s *Server) Directory() *directory.Directory { return s.state.Load().dir }

// SignIn is the sign-in checker of mail programs (IMAP, SMTP, DAV).
func (s *Server) SignIn() *signin.Checker { return s.signIn }

// Store is the mail storage (for IMAP).
func (s *Server) Store() *storage.Store { return s.store }

func (s *Server) resume() error {
	b, err := os.ReadFile(filepath.Join(s.opts.DataDir, configFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg config.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return s.apply(&cfg)
}

// apply swaps in a validated configuration.
func (s *Server) apply(cfg *config.Config) error {
	certs, err := parseCerts(cfg.Certificates)
	if err != nil {
		return err
	}
	dir := directory.New(directory.Merge(cfg, s.overlays))
	s.state.Store(&runtime{pushed: cfg, dir: dir, hash: cfg.Hash(), certs: certs})
	s.logIgnoredGrants(dir.Ignored())
	return nil
}

// logIgnoredGrants logs the grants a configuration has that do not count
// (another organisation, a deleted or shared mailbox), once for as long as
// they stay the same.
func (s *Server) logIgnoredGrants(notes []string) {
	key := strings.Join(notes, "\n")
	s.ignoredMu.Lock()
	defer s.ignoredMu.Unlock()
	if key == s.ignored {
		return
	}
	s.ignored = key
	for _, n := range notes {
		s.log.Warn("mail: grant ignored", "what", n)
	}
}

// Load validates, binds, persists and applies a configuration.
func (s *Server) Load(ctx context.Context, cfg *config.Config) error {
	if err := cfg.Validate(); err != nil {
		return &loadError{err}
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	bindings := make(map[string]string, len(cfg.Mailboxes))
	for _, m := range cfg.Mailboxes {
		bindings[m.StorageID] = m.Address
	}
	if err := s.store.Bind(ctx, bindings); err != nil {
		var c *storage.BindingConflictError
		if errors.As(err, &c) {
			return &loadError{err}
		}
		return err
	}
	if err := s.pruneOverlays(ctx, cfg); err != nil {
		return err
	}
	hash := cfg.Hash()
	if hash != s.state.Load().hash {
		if err := s.persist(cfg); err != nil {
			return err
		}
	}
	if err := s.apply(cfg); err != nil {
		return &loadError{err}
	}
	s.syncListeners()
	s.log.Info("mail: configuration loaded", "domains", len(cfg.Domains), "mailboxes", len(cfg.Mailboxes), "aliases", len(cfg.Aliases))
	return nil
}

// loadError is a configuration the panel must fix (400).
type loadError struct{ err error }

func (e *loadError) Error() string { return e.err.Error() }
func (e *loadError) Unwrap() error { return e.err }

// persist writes config.json atomically and durably (it holds secrets: 0600).
func (s *Server) persist(cfg *config.Config) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.opts.DataDir, ".config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after the rename
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.opts.DataDir, configFile)); err != nil {
		return err
	}
	d, err := os.Open(s.opts.DataDir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// ready tells whether the configuration allows opening ports: a host
// name, a certificate and at least one domain.
func (rt *runtime) ready() bool {
	cfg := rt.dir.Config()
	return cfg.Hostname != "" && len(rt.certs.certs) > 0 && len(cfg.Domains) > 0
}

const notReady = "waiting for the mail host name, a certificate and a domain"

// syncListeners opens, reconfigures or closes the listeners to match the
// current configuration.
func (s *Server) syncListeners() {
	s.lisMu.Lock()
	defer s.lisMu.Unlock()
	rt := s.state.Load()
	for _, st := range s.allListeners() {
		s.syncListener(rt, st)
	}
}

func (s *Server) syncListener(rt *runtime, st *listenerState) {
	if st.cfg == "" {
		st.addr, st.err = "", "disabled"
		return
	}
	if !rt.ready() {
		if st.srv != nil {
			srv := st.srv
			st.srv = nil
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
				defer cancel()
				srv.Shutdown(ctx)
			}()
		}
		st.addr, st.err = st.cfg, notReady
		return
	}
	if st.srv != nil {
		st.srv.Reconfigure()
		return
	}
	ln, err := net.Listen("tcp", st.cfg)
	if err != nil {
		st.addr, st.err = st.cfg, bindError(err)
		if st.reportErr != st.err {
			st.reportErr = st.err
			s.log.Error("mail: cannot listen", "listener", st.name, "addr", st.cfg, "err", st.err)
			s.event(config.Event{Kind: config.EventListenerFailed, Detail: fmt.Sprintf("port %s (%s): %s", st.cfg, st.label, st.err)})
		}
		return
	}
	srv := st.start()
	st.srv, st.addr, st.err, st.reportErr = srv, ln.Addr().String(), "", ""
	s.log.Info("mail: listening", "listener", st.name, "addr", st.addr)
	go func() {
		if err := srv.Serve(ln); err != nil {
			s.log.Error("mail: listener stopped", "listener", st.name, "err", err)
			s.lisMu.Lock()
			if st.srv == srv {
				st.srv, st.err = nil, err.Error()
			}
			s.lisMu.Unlock()
		}
	}()
}

func (s *Server) startSMTP() service {
	return smtpd.New(smtpd.Options{
		Store:     s.store,
		Directory: s.Directory,
		TLSConfig: s.tls,
		Logger:    s.log,
		FreeBytes: s.opts.FreeBytes,
		Filter:    s.filter,
		Queue:     s.queue,
		SRS:       s.srs,
	})
}

func (s *Server) allListeners() []*listenerState {
	return []*listenerState{&s.smtp, &s.submission, &s.submissions, &s.imaps}
}

func (s *Server) startSubmission(implicitTLS bool) service {
	return submission.New(submission.Options{
		Store:       s.store,
		Directory:   s.Directory,
		SignIn:      s.signIn,
		Limits:      s.limits,
		Queue:       s.queue,
		Signer:      s.signer,
		TLSConfig:   s.tls,
		ImplicitTLS: implicitTLS,
		Logger:      s.log,
		FreeBytes:   s.opts.FreeBytes,
	})
}

func (s *Server) startIMAPS() service {
	return imapd.New(imapd.Options{
		Store:     s.store,
		Directory: s.Directory,
		SignIn:    s.signIn,
		TLSConfig: s.tls,
		Logger:    s.log,
		FreeBytes: s.opts.FreeBytes,
	})
}

func bindError(err error) string {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return "address already in use"
	case errors.Is(err, syscall.EACCES):
		return "permission denied"
	}
	return err.Error()
}

// SMTPAddr is the address the SMTP listener is bound to ("" if none).
func (s *Server) SMTPAddr() string { return s.boundAddr(&s.smtp) }

// SubmissionAddr is the address the submission (STARTTLS) listener is
// bound to ("" if none).
func (s *Server) SubmissionAddr() string { return s.boundAddr(&s.submission) }

// SubmissionsAddr is the address the submission (TLS) listener is bound to
// ("" if none).
func (s *Server) SubmissionsAddr() string { return s.boundAddr(&s.submissions) }

// Queue is the outbound queue.
func (s *Server) Queue() *queue.Queue { return s.queue }

// IMAPSAddr is the address the IMAPS listener is bound to ("" if none).
func (s *Server) IMAPSAddr() string { return s.boundAddr(&s.imaps) }

func (s *Server) boundAddr(st *listenerState) string {
	s.lisMu.Lock()
	defer s.lisMu.Unlock()
	if st.srv == nil {
		return ""
	}
	return st.addr
}

// RecordEvent records an event for the panel (the self-service area's
// sign-in failures). Detail must not contain secrets.
func (s *Server) RecordEvent(e config.Event) { s.event(e) }

func (s *Server) event(e config.Event) {
	if err := s.store.AddEvent(context.Background(), e); err != nil {
		s.log.Error("mail: record event", "err", err)
	}
}

// Status reports the service's state.
func (s *Server) Status(ctx context.Context) (config.Status, error) {
	rt := s.state.Load()
	st := config.Status{ConfigHash: rt.hash, Listeners: s.listeners(), Web: s.webStatus(), Mailboxes: []config.MailboxStatus{}, Orphans: []string{}}
	if len(rt.certs.certs) > 0 {
		st.CertificateExpires = rt.certs.certs[0].Leaf.NotAfter.UTC()
	}
	var err error
	if st.QueueLength, err = s.queue.Len(ctx); err != nil {
		return st, err
	}
	blocks, err := s.store.Blocks(ctx)
	if err != nil {
		return st, err
	}
	configured := map[string]bool{}
	for _, m := range rt.dir.Config().Mailboxes {
		configured[m.StorageID] = true
		u, err := s.store.Usage(ctx, m.StorageID)
		if err != nil {
			return st, err
		}
		b := blocks[m.StorageID]
		st.Mailboxes = append(st.Mailboxes, config.MailboxStatus{
			StorageID: m.StorageID, UsedBytes: u.Bytes, Messages: u.Messages,
			Blocked: b.Blocked, BlockedReason: b.Reason, BlockedAt: b.At,
		})
	}
	ids, err := s.store.StorageIDs(ctx)
	if err != nil {
		return st, err
	}
	for _, id := range ids {
		if !configured[id] {
			st.Orphans = append(st.Orphans, id)
		}
	}
	return st, nil
}

func (s *Server) listeners() []config.Listener {
	s.lisMu.Lock()
	defer s.lisMu.Unlock()
	report := func(st *listenerState) config.Listener {
		l := config.Listener{Name: st.name, Address: st.addr, Listening: st.srv != nil, Error: st.err}
		if l.Address == "" {
			l.Address = st.cfg
		}
		return l
	}
	var out []config.Listener
	for _, st := range s.allListeners() {
		out = append(out, report(st))
	}
	return out
}

// webStatus is the state of the web socket, nil without one.
func (s *Server) webStatus() *config.Listener {
	if s.opts.WebSocket == "" {
		return nil
	}
	s.lisMu.Lock()
	defer s.lisMu.Unlock()
	return &config.Listener{Name: "web", Address: s.opts.WebSocket, Listening: s.webErr == "" && s.web != nil, Error: s.webErr}
}

// webFailed records that the web socket does not work. Mail goes on.
func (s *Server) webFailed(err error) {
	s.lisMu.Lock()
	s.webErr = err.Error()
	s.lisMu.Unlock()
	s.log.Error("mail: running without the web socket", "err", err)
	s.event(config.Event{Kind: config.EventListenerFailed, Detail: "sign-in page for mailbox users: " + err.Error()})
}

// Run serves the admin socket and the listeners until ctx ends, then shuts
// down gracefully.
func (s *Server) Run(ctx context.Context) error {
	if err := s.listenAdmin(); err != nil {
		s.store.Close()
		return err
	}
	// The web socket is optional for mail: if it cannot be set up the
	// service runs without it, and its status says why.
	if err := s.listenWeb(); err != nil {
		s.webFailed(err)
	}
	adminDone := make(chan error, 1)
	go func() { adminDone <- s.admin.Serve(s.adminLn) }()
	webDone := make(chan error, 1)
	if s.web != nil {
		go func() { webDone <- s.web.Serve(s.webLn) }()
	}
	s.syncListeners()
	s.store.PruneEvents(ctx)
	s.limits.Prune(ctx)
	s.filter.Prune(ctx)
	s.queue.Start()
	if err := s.imports.Start(ctx); err != nil {
		s.log.Error("mail: start imports", "err", err)
	}

	retry := time.NewTicker(30 * time.Second)
	defer retry.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.shutdown()
		case err := <-adminDone:
			if !errors.Is(err, http.ErrServerClosed) {
				s.shutdown()
				return fmt.Errorf("admin socket: %w", err)
			}
		case err := <-webDone:
			if !errors.Is(err, http.ErrServerClosed) {
				s.webFailed(fmt.Errorf("web socket: %w", err))
			}
		case <-retry.C:
			// A port that was busy (another mail server) may be free now.
			s.syncListeners()
		case <-prune.C:
			if err := s.store.PruneEvents(context.Background()); err != nil {
				s.log.Error("mail: prune events", "err", err)
			}
			if err := s.limits.Prune(context.Background()); err != nil {
				s.log.Error("mail: prune send log", "err", err)
			}
			if err := s.filter.Prune(context.Background()); err != nil {
				s.log.Error("mail: prune greylist", "err", err)
			}
			if err := s.imports.Prune(context.Background()); err != nil {
				s.log.Error("mail: prune imports", "err", err)
			}
		}
	}
}

func (s *Server) listenAdmin() error {
	ln, err := listenUnix(s.opts.AdminSocket)
	if err != nil {
		return err
	}
	s.adminLn = ln
	s.admin = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	return nil
}

// shutdown stops the listeners (running transactions may finish, bounded),
// the admin socket and the storage.
func (s *Server) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	s.lisMu.Lock()
	var srvs []service
	for _, st := range s.allListeners() {
		if st.srv != nil {
			srvs = append(srvs, st.srv)
			st.srv = nil
		}
	}
	s.lisMu.Unlock()
	var wg sync.WaitGroup
	for _, srv := range srvs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.Shutdown(ctx)
		}()
	}
	wg.Wait()
	// Deliveries still running when the grace time is over are cancelled
	// and tried again after the next start.
	s.queue.Shutdown(ctx)
	// Imports stop at the next message; they continue (files) or are
	// interrupted (IMAP) after the next start.
	s.imports.Shutdown(ctx)
	if s.web != nil {
		wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.web.Shutdown(wctx); err != nil {
			s.web.Close()
		}
		wcancel()
		os.Remove(s.opts.WebSocket)
	}
	if s.admin != nil {
		actx, acancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.admin.Shutdown(actx); err != nil {
			s.admin.Close() // uploads and exports still running are cut off
		}
		acancel()
		os.Remove(s.opts.AdminSocket)
	}
	return s.store.Close()
}

// checkOutbound tries port 25 of gmail.com's first MX.
func checkOutbound(ctx context.Context) config.OutboundCheck {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	mx, err := net.DefaultResolver.LookupMX(ctx, "gmail.com")
	if err != nil || len(mx) == 0 {
		return config.OutboundCheck{Target: "gmail.com", Error: "could not look up the mail servers of gmail.com"}
	}
	target := net.JoinHostPort(trimDot(mx[0].Host), "25")
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		msg := "connection failed"
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
			msg = "no answer within 10 seconds (outgoing port 25 is probably blocked)"
		} else if errors.Is(err, syscall.ECONNREFUSED) {
			msg = "connection refused"
		}
		return config.OutboundCheck{Target: target, Error: msg}
	}
	c.Close()
	return config.OutboundCheck{OK: true, Target: target}
}

func trimDot(h string) string { return strings.TrimSuffix(h, ".") }
