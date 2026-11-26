// Package smtpd receives mail from other servers on port 25 and stores it
// for the configured mailboxes. It accepts only recipients this server is
// responsible for and answers "250" only once the message is durable.
//
// Every message is checked (package auth: SPF, DKIM, DMARC; package
// filter: blocklists, greylisting, content score, attachments) and gets an
// Authentication-Results and an X-Ownpanel-Score header; Junk goes to the
// Junk folder. External alias targets are forwarded through the queue with
// an SRS envelope sender, and bounces to our SRS addresses go back to the
// original sender.
package smtpd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/filter"
	"github.com/danielgtmn/ownpanel/internal/mail/queue"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Defaults for Options.
const (
	DefaultIdleTimeout    = 5 * time.Minute
	DefaultWriteTimeout   = time.Minute
	DefaultMaxConnections = 1000
	// DefaultMinFreeBytes: below this much free disk, mail is refused with
	// a temporary error.
	DefaultMinFreeBytes = 1 << 30
)

// Options configure a Server.
type Options struct {
	Store *storage.Store
	// Directory returns the current configuration view.
	Directory func() *directory.Directory
	// TLSConfig enables STARTTLS when set. Its certificates may change
	// (GetCertificate); the Config itself must not.
	TLSConfig *tls.Config
	Logger    *slog.Logger

	IdleTimeout    time.Duration
	WriteTimeout   time.Duration
	MaxConnections int
	MinFreeBytes   int64
	// FreeBytes reports free disk space (tests); nil means Store.FreeBytes.
	FreeBytes func() (int64, error)

	// NewSession replaces the port 25 sessions (the submission server
	// uses this server's listener, limits and generations with its own
	// sessions). Nil means port 25.
	NewSession func(c *smtp.Conn) (smtp.Session, error)
	// ImplicitTLS starts every connection with TLS (port 465); TLSConfig
	// must be set. Refused connections are closed without a reply then.
	ImplicitTLS bool

	// Filter runs blocklists, greylisting and the content checks; nil
	// means one with the system resolver.
	Filter *filter.Filter
	// Queue takes forwarded mail and bounces routed back to SRS
	// addresses. Without it, addresses that forward outside are refused.
	Queue *queue.Queue
	// SRS rewrites the envelope sender of forwarded mail; nil means one
	// with a random key (bounces then fail after a restart).
	SRS *auth.SRS
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
	// ClientIP returns the address a connection comes from (tests); nil
	// means the connection's remote address.
	ClientIP func(net.Addr) netip.Addr
}

// Server is the port 25 server. It runs go-smtp servers behind its own
// accept loop: a new go-smtp server is started when the hostname or limits
// change (go-smtp reads them without locking), while connections already
// open finish on the one they started with.
type Server struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	gen      *generation
	old      sync.WaitGroup // retiring generations
	ln       *limitListener
	stopping atomic.Bool
	done     chan struct{}
}

type generation struct {
	key string
	srv *smtp.Server
	ln  *chanListener
	run sync.WaitGroup
}

// New returns a server; call Serve to run it.
func New(opts Options) *Server {
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	if opts.MaxConnections <= 0 {
		opts.MaxConnections = DefaultMaxConnections
	}
	if opts.MinFreeBytes == 0 {
		opts.MinFreeBytes = DefaultMinFreeBytes
	}
	if opts.FreeBytes == nil {
		opts.FreeBytes = opts.Store.FreeBytes
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Filter == nil {
		opts.Filter = filter.New(filter.Options{Store: opts.Store, Logger: opts.Logger})
	}
	if opts.SRS == nil {
		opts.SRS = auth.NewSRS(randomKey(), opts.Now)
	}
	if opts.ClientIP == nil {
		opts.ClientIP = func(a net.Addr) netip.Addr {
			ap, err := netip.ParseAddrPort(a.String())
			if err != nil {
				return netip.Addr{}
			}
			return ap.Addr().Unmap()
		}
	}
	return &Server{opts: opts, log: opts.Logger, done: make(chan struct{})}
}

// Serve accepts connections on l until Shutdown or Close. It returns nil
// after a shutdown.
func (s *Server) Serve(l net.Listener) error {
	d := s.opts.Directory()
	s.mu.Lock()
	if s.ln != nil {
		s.mu.Unlock()
		return errors.New("smtpd: already serving")
	}
	s.ln = newLimitListener(l, d.Limits().ConnectionsPerIP, s.opts.MaxConnections, s.opts.IdleTimeout)
	refusal := func(line string) string { return line }
	if s.opts.ImplicitTLS {
		s.ln.refusal = ""
		refusal = func(string) string { return "" }
	}
	ln := s.ln
	s.mu.Unlock()
	s.Reconfigure()
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return err
		}
		if s.stopping.Load() {
			go refuse(c, refusal("421 4.3.2 Service shutting down, try again later\r\n"))
			continue
		}
		if s.opts.ImplicitTLS {
			c = tls.Server(c, s.opts.TLSConfig)
		}
		for {
			g := s.current()
			if g == nil {
				refuse(c, refusal("421 4.3.0 Service not ready\r\n"))
				break
			}
			if g.ln.deliver(c) {
				break
			}
		}
	}
}

func (s *Server) current() *generation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// Reconfigure applies a new configuration: connection limits at once, and
// a new go-smtp server for new connections if the hostname or the message
// limits changed.
func (s *Server) Reconfigure() {
	d := s.opts.Directory()
	lim := d.Limits()
	key := d.Hostname() + "|" + strconv.FormatInt(lim.MaxMessageBytes, 10) + "|" + strconv.Itoa(lim.MaxRecipients)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil || s.stopping.Load() {
		return
	}
	s.ln.perIP.Store(int64(lim.ConnectionsPerIP))
	if s.gen != nil && s.gen.key == key {
		return
	}
	g := &generation{key: key, ln: newChanListener(s.ln.Addr())}
	newSession := s.newSession
	if s.opts.NewSession != nil {
		newSession = s.opts.NewSession
	}
	srv := smtp.NewServer(smtp.BackendFunc(newSession))
	srv.Domain = d.Hostname()
	srv.MaxMessageBytes = lim.MaxMessageBytes
	srv.MaxRecipients = lim.MaxRecipients
	srv.WriteTimeout = s.opts.WriteTimeout
	srv.TLSConfig = s.opts.TLSConfig
	srv.ErrorLog = errorLog{s.log}
	g.srv = srv
	g.run.Add(1)
	go func() {
		defer g.run.Done()
		srv.Serve(g.ln)
	}()
	if old := s.gen; old != nil {
		s.old.Add(1)
		go func() {
			defer s.old.Done()
			s.retire(old, context.Background())
		}()
	}
	s.gen = g
}

// shutdownGrace bounds how long running transactions may take to finish.
const shutdownGrace = 30 * time.Second

func (s *Server) retire(g *generation, ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, shutdownGrace)
	defer cancel()
	if err := g.srv.Shutdown(ctx); err != nil {
		g.srv.Close()
	}
	// go-smtp closes only listeners its Serve has registered. A generation
	// retired right after it was started (a configuration change followed
	// by a shutdown) may not have got that far, and its Accept would wait
	// forever; closing our own listener ends it either way.
	g.ln.Close()
	g.run.Wait()
}

// Stopping tells whether Shutdown or Close was called; sessions refuse
// new transactions then.
func (s *Server) Stopping() bool { return s.stopping.Load() }

// Shutdown stops taking new mail: new connections and new transactions get
// 421, running transactions may finish until ctx ends (at most 30 s), then
// everything is closed.
func (s *Server) Shutdown(ctx context.Context) error {
	if !s.stopping.CompareAndSwap(false, true) {
		return nil
	}
	s.mu.Lock()
	g := s.gen
	s.gen = nil
	s.mu.Unlock()
	if g != nil {
		s.retire(g, ctx)
	}
	s.old.Wait()
	return s.closeListener()
}

// Close stops at once.
func (s *Server) Close() error {
	s.stopping.Store(true)
	s.mu.Lock()
	g := s.gen
	s.gen = nil
	s.mu.Unlock()
	if g != nil {
		g.srv.Close()
		g.run.Wait()
	}
	return s.closeListener()
}

func (s *Server) closeListener() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	default:
		close(s.done)
	}
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

// errorLog sends go-smtp's connection errors (resets, timeouts) to slog at
// debug level: they are normal on the internet.
type errorLog struct{ l *slog.Logger }

func (e errorLog) Printf(format string, v ...any) {
	e.l.Debug("smtp", "msg", fmt.Sprintf(format, v...))
}
func (e errorLog) Println(v ...any) { e.l.Debug("smtp", "msg", fmt.Sprint(v...)) }
