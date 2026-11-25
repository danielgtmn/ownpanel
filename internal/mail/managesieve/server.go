// Package managesieve lets mail programs manage the filter scripts of a
// mailbox (ManageSieve, RFC 5804) on port 4190.
//
// Connections start in plain text and must switch to TLS with STARTTLS
// before anything else: until then only CAPABILITY, STARTTLS, NOOP and
// LOGOUT work, and no sign-in mechanism is offered. Sign-in is AUTHENTICATE
// PLAIN through internal/mail/signin, exactly like IMAP (passwords and app
// passwords, the same throttle). Scripts are checked with package sieve
// and stored through internal/mail/storage; only valid scripts are kept.
package managesieve

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"

	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/smtpd"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// DefaultMaxConnections bounds the connections of all clients together.
const DefaultMaxConnections = 500

// Options configure a Server.
type Options struct {
	Store *storage.Store
	// Directory returns the current configuration view.
	Directory func() *directory.Directory
	// SignIn checks passwords; shared with IMAP and submission.
	SignIn *signin.Checker
	// TLSConfig provides the certificates for STARTTLS.
	TLSConfig *tls.Config
	Logger    *slog.Logger

	MaxConnections int
}

// Server serves ManageSieve.
type Server struct {
	opts Options
	log  *slog.Logger
	tls  *tls.Config

	mu       sync.Mutex
	ln       *smtpd.LimitListener
	sessions map[*session]struct{}
	stopping bool
	wg       sync.WaitGroup
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
	tc := opts.TLSConfig.Clone()
	tc.MinVersion = tls.VersionTLS12
	return &Server{opts: opts, log: opts.Logger, tls: tc, sessions: map[*session]struct{}{}}
}

// refusal answers connections over the limits.
const refusal = "BYE (TRYLATER) \"Too many connections, try again later\"\r\n"

// Serve accepts connections on l (plain TCP) until Shutdown or Close. It
// returns nil after those.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.ln != nil || s.stopping {
		s.mu.Unlock()
		return errors.New("managesieve: already serving or stopped")
	}
	lim := smtpd.NewLimitListener(l, s.opts.Directory().Limits().ConnectionsPerIP, s.opts.MaxConnections, 0, refusal)
	s.ln = &lim
	s.mu.Unlock()
	for {
		c, err := lim.Accept()
		if err != nil {
			s.mu.Lock()
			stopping := s.stopping
			s.mu.Unlock()
			if stopping || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		sess := newSession(s, c)
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			c.Close()
			continue
		}
		s.sessions[sess] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer s.remove(sess)
			sess.serve()
		}()
	}
}

func (s *Server) remove(sess *session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}

// Reconfigure applies a new configuration: the connection limit per IP
// for new connections, and it ends the sessions of mailboxes that are gone
// or whose password or app password is no longer valid.
func (s *Server) Reconfigure() {
	dir := s.opts.Directory()
	s.mu.Lock()
	if s.ln != nil {
		s.ln.SetPerIP(dir.Limits().ConnectionsPerIP)
	}
	var stale []*session
	for sess := range s.sessions {
		if g, ok := sess.grant(); ok && !signin.Valid(dir, g) {
			stale = append(stale, sess)
		}
	}
	s.mu.Unlock()
	for _, sess := range stale {
		go sess.bye("", "Sign-in no longer valid")
	}
}

// Shutdown says BYE to every session and closes everything; it waits for
// the sessions to end until ctx is done. Commands are short and clients
// simply reconnect.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	ln := s.ln
	all := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		all = append(all, sess)
	}
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, sess := range all {
		go sess.bye("", "Server shutting down")
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		s.mu.Lock()
		for sess := range s.sessions {
			sess.conn().Close()
		}
		s.mu.Unlock()
	}
	return nil
}
