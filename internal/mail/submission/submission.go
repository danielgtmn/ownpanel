// Package submission is the server mail clients send through: port 587
// (STARTTLS required before AUTH) and 465 (implicit TLS), both with AUTH
// PLAIN and LOGIN checked by internal/mail/signin.
//
// A mailbox may send as its own address or as an alias that delivers to it
// (the catch-all does not count), checked for the envelope sender and for
// every address in the header From. Accepted messages get a Received
// header, a Message-ID and Date when missing, and a DKIM signature with the
// active key of the From domain. Recipients on this server are delivered
// directly into their INBOX; all others go into the outbound queue.
//
// Each go-smtp server of a port runs behind smtpd's listener (connection
// limits, idle timeout, new server generations on configuration changes);
// the sessions are this package's.
package submission

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/deliver"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/queue"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/smtpd"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Options configure a Server.
type Options struct {
	Store *storage.Store
	// Directory returns the current configuration view.
	Directory func() *directory.Directory
	// SignIn checks passwords; shared with IMAP.
	SignIn *signin.Checker
	// Limits counts sent messages and blocks mailboxes; shared by both
	// ports.
	Limits *Limiter
	Queue  *queue.Queue
	Signer *auth.Signer
	// TLSConfig provides the certificates (GetCertificate).
	TLSConfig *tls.Config
	// ImplicitTLS: port 465. Otherwise STARTTLS is offered and required
	// before AUTH (port 587).
	ImplicitTLS bool
	Logger      *slog.Logger

	// FreeBytes reports free disk space (tests); nil means Store.FreeBytes.
	FreeBytes    func() (int64, error)
	MinFreeBytes int64
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
	// Deliver stores mail for mailboxes of this server and runs their
	// filters; nil means one with Store, Queue and Signer.
	Deliver *deliver.Deliverer
}

// Server is one submission port.
type Server struct {
	opts Options
	log  *slog.Logger
	smtp *smtpd.Server
}

// New returns a server; call Serve to run it.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MinFreeBytes == 0 {
		opts.MinFreeBytes = smtpd.DefaultMinFreeBytes
	}
	if opts.FreeBytes == nil {
		opts.FreeBytes = opts.Store.FreeBytes
	}
	if opts.Signer == nil {
		opts.Signer = auth.NewSigner()
	}
	if opts.Deliver == nil {
		opts.Deliver = deliver.New(deliver.Options{Store: opts.Store, Queue: opts.Queue, Signer: opts.Signer, Logger: opts.Logger, Now: opts.Now})
	}
	tc := opts.TLSConfig.Clone()
	tc.MinVersion = tls.VersionTLS12
	opts.TLSConfig = tc
	s := &Server{opts: opts, log: opts.Logger}
	s.smtp = smtpd.New(smtpd.Options{
		Store:       opts.Store,
		Directory:   opts.Directory,
		TLSConfig:   tc,
		Logger:      opts.Logger,
		FreeBytes:   opts.FreeBytes,
		NewSession:  s.newSession,
		ImplicitTLS: opts.ImplicitTLS,
	})
	return s
}

// Serve accepts connections on l (plain TCP; TLS is added here for port
// 465) until Shutdown or Close.
func (s *Server) Serve(l net.Listener) error { return s.smtp.Serve(l) }

// Reconfigure applies a new configuration (limits, host name). Sessions
// check their sign-in again at the next transaction.
func (s *Server) Reconfigure() { s.smtp.Reconfigure() }

// Shutdown refuses new transactions and lets running ones finish until ctx
// ends.
func (s *Server) Shutdown(ctx context.Context) error { return s.smtp.Shutdown(ctx) }

// Close stops at once.
func (s *Server) Close() error { return s.smtp.Close() }

func (s *Server) newSession(c *smtp.Conn) (smtp.Session, error) {
	return newSession(s, c), nil
}
