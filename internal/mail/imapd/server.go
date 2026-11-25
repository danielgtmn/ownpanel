// Package imapd is the IMAP server of the mail service: implicit TLS only
// (port 993, RFC 8314), on top of internal/mail/storage, with sign-in
// through internal/mail/signin.
//
// Every session keeps its own view of the selected folder (the UIDs in
// the order the client knows them, so sequence numbers stay stable) and a
// queue of the changes the storage reports through Watch. The queue is
// applied when the protocol allows it: after commands, during IDLE, and
// without EXPUNGE responses after FETCH, STORE and SEARCH. The storage is
// the only source of truth; changes of other sessions and new deliveries
// reach a session the same way as its own.
package imapd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/smtpd"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// DefaultMaxConnections bounds the connections of all clients together.
const DefaultMaxConnections = 2000

// Options configure a Server.
type Options struct {
	Store *storage.Store
	// Directory returns the current configuration view.
	Directory func() *directory.Directory
	// SignIn checks passwords; shared with submission.
	SignIn *signin.Checker
	// TLSConfig provides the certificates (GetCertificate). Connections
	// start with TLS; there is no plain-text port.
	TLSConfig *tls.Config
	Logger    *slog.Logger
	// FreeBytes reports free disk space (tests); nil means Store.FreeBytes.
	// Below MinFreeBytes (0: smtpd.DefaultMinFreeBytes) APPEND is refused
	// for now, like mail arriving over SMTP and submission.
	FreeBytes    func() (int64, error)
	MinFreeBytes int64

	MaxConnections int
}

// Server serves IMAP over TLS.
//
// Timeouts come from the library: 30 s for a command line before sign-in,
// 35 minutes without a command afterwards and in IDLE (RFC 9051: at least
// 30), 5 minutes per literal. Literals other than APPEND are limited to 4
// KiB, APPEND to the configured message size.
type Server struct {
	opts Options
	log  *slog.Logger
	imap *imapserver.Server

	mu       sync.Mutex
	ln       *smtpd.LimitListener
	sessions map[*session]struct{}
	stopping bool
	closed   bool
}

// Capabilities offered. IDLE, UNSELECT, ENABLE, SASL-IR, LITERAL- and
// APPENDLIMIT are added by the library.
var caps = imap.CapSet{
	imap.CapIMAP4rev1:    {},
	imap.CapIMAP4rev2:    {},
	imap.CapNamespace:    {},
	imap.CapUIDPlus:      {},
	imap.CapESearch:      {},
	imap.CapSearchRes:    {},
	imap.CapListExtended: {},
	imap.CapListStatus:   {},
	imap.CapMove:         {},
	imap.CapStatusSize:   {},
	imap.CapBinary:       {},
	imap.CapChildren:     {},
	imap.CapSpecialUse:   {},
	// Not LITERAL+: with it a client may send a message over the size
	// limit without waiting, and the library cannot skip it then.
}

// New returns a server; call Serve to run it.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxConnections <= 0 {
		opts.MaxConnections = DefaultMaxConnections
	}
	if opts.SignIn == nil {
		opts.SignIn = signin.New(signin.Options{})
	}
	if opts.MinFreeBytes == 0 {
		opts.MinFreeBytes = smtpd.DefaultMinFreeBytes
	}
	if opts.FreeBytes == nil {
		opts.FreeBytes = opts.Store.FreeBytes
	}
	s := &Server{opts: opts, log: opts.Logger, sessions: map[*session]struct{}{}}
	s.imap = imapserver.New(&imapserver.Options{
		NewSession: s.newSession,
		Caps:       caps,
		Logger:     errorLog{s.log},
	})
	return s
}

// Serve accepts connections on l (plain TCP; TLS is added here) until
// Shutdown or Close. It returns nil after those.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.ln != nil || s.stopping {
		s.mu.Unlock()
		return errors.New("imapd: already serving or stopped")
	}
	lim := smtpd.NewLimitListener(l, s.opts.Directory().Limits().ConnectionsPerIP, s.opts.MaxConnections, 0, "")
	s.ln = &lim
	s.mu.Unlock()
	tc := s.opts.TLSConfig.Clone()
	tc.MinVersion = tls.VersionTLS12
	err := s.imap.Serve(tls.NewListener(lim, tc))
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Reconfigure applies a new configuration: the connection limit per IP
// for new connections, and it ends the sessions of mailboxes that are gone
// or whose password changed.
func (s *Server) Reconfigure() {
	dir := s.opts.Directory()
	s.mu.Lock()
	if s.ln != nil {
		s.ln.SetPerIP(dir.Limits().ConnectionsPerIP)
	}
	var stale, live []*session
	for sess := range s.sessions {
		if box, ok := sess.mailbox(); ok && !signin.Valid(dir, box) {
			stale = append(stale, sess)
		} else if ok {
			live = append(live, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range stale {
		go sess.end()
	}
	// Grants may have changed: watches follow them, and a session whose
	// selected shared folder is no longer readable ends.
	for _, sess := range live {
		sess.queueReconfigure(dir)
	}
}

// Shutdown says BYE to every session (bounded by ctx) and closes
// everything. IMAP has no transaction worth waiting for: clients
// reconnect and resynchronize.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	all := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		all = append(all, sess)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, sess := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess.conn.Bye("Server shutting down")
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return s.Close()
}

// Close stops at once.
func (s *Server) Close() error {
	s.mu.Lock()
	s.stopping = true
	closed := s.closed
	s.closed = true
	ln := s.ln
	s.mu.Unlock()
	if closed {
		return nil
	}
	err := s.imap.Close()
	if ln != nil {
		ln.Close() // in case Serve has not registered it with the library yet
	}
	return err
}

func (s *Server) newSession(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
	ip, _ := netip.ParseAddrPort(c.NetConn().RemoteAddr().String())
	sess := &session{srv: s, conn: c, ip: ip.Addr().Unmap(), wake: make(chan struct{}, 1)}
	sess.ctx = storage.WithOrigin(context.Background(), sess)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, nil, &imap.Error{Type: imap.StatusResponseTypeBye, Text: "Server shutting down"}
	}
	s.sessions[sess] = struct{}{}
	return sess, nil, nil
}

func (s *Server) remove(sess *session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}

// errorLog sends the library's messages (resets, timeouts, client
// errors) to slog at debug level.
type errorLog struct{ l *slog.Logger }

func (e errorLog) Printf(format string, v ...any) {
	e.l.Debug("imap", "msg", fmt.Sprintf(format, v...))
}
