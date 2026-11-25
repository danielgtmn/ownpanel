package mailtest

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
)

// SinkTLS says what a Sink offers.
type SinkTLS int

const (
	// SinkPlain offers no STARTTLS.
	SinkPlain SinkTLS = iota
	// SinkStartTLS offers STARTTLS with a self-signed certificate.
	SinkStartTLS
	// SinkBrokenTLS offers STARTTLS but every handshake fails.
	SinkBrokenTLS
)

// SinkMessage is a message a Sink received.
type SinkMessage struct {
	Helo string
	From string
	To   []string
	Data []byte
	TLS  bool
}

// Sink is an SMTP server for tests of outgoing mail.
type Sink struct {
	Addr string // 127.0.0.1:port
	Port string

	mu     sync.Mutex
	msgs   []SinkMessage
	rcpt   func(to string) error
	data   func() error
	notify chan struct{}
}

// NewSink starts a sink on 127.0.0.1; it stops with the test.
func NewSink(t testing.TB, mode SinkTLS) *Sink {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Sink{Addr: ln.Addr().String(), notify: make(chan struct{}, 100)}
	_, s.Port, _ = net.SplitHostPort(s.Addr)
	srv := smtp.NewServer(smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) {
		return &sinkSession{s: s, c: c}, nil
	}))
	srv.Domain = "sink.test"
	srv.AllowInsecureAuth = true
	switch mode {
	case SinkStartTLS:
		c := Cert(t, "sink.test", false)
		kp, err := tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM))
		if err != nil {
			t.Fatal(err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{kp}}
	case SinkBrokenTLS:
		srv.TLSConfig = &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return nil, io.ErrUnexpectedEOF
		}}
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return s
}

// SetRcpt sets the answer per recipient (nil: accept all).
func (s *Sink) SetRcpt(f func(to string) error) {
	s.mu.Lock()
	s.rcpt = f
	s.mu.Unlock()
}

// SetData sets the answer to the message (nil: accept).
func (s *Sink) SetData(f func() error) {
	s.mu.Lock()
	s.data = f
	s.mu.Unlock()
}

// Messages returns what was received so far.
func (s *Sink) Messages() []SinkMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SinkMessage(nil), s.msgs...)
}

// Wait waits until n messages were received or the timeout passes.
func (s *Sink) Wait(n int, timeout time.Duration) []SinkMessage {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if m := s.Messages(); len(m) >= n {
			return m
		}
		select {
		case <-s.notify:
		case <-ctx.Done():
			return s.Messages()
		}
	}
}

type sinkSession struct {
	s    *Sink
	c    *smtp.Conn
	from string
	to   []string
}

func (ss *sinkSession) Reset()        { ss.from, ss.to = "", nil }
func (ss *sinkSession) Logout() error { return nil }
func (ss *sinkSession) Mail(from string, _ *smtp.MailOptions) error {
	ss.from = from
	return nil
}

func (ss *sinkSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	ss.s.mu.Lock()
	f := ss.s.rcpt
	ss.s.mu.Unlock()
	if f != nil {
		if err := f(to); err != nil {
			return err
		}
	}
	ss.to = append(ss.to, to)
	return nil
}

func (ss *sinkSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	ss.s.mu.Lock()
	f := ss.s.data
	ss.s.mu.Unlock()
	if f != nil {
		if err := f(); err != nil {
			return err
		}
	}
	_, isTLS := ss.c.TLSConnectionState()
	ss.s.mu.Lock()
	ss.s.msgs = append(ss.s.msgs, SinkMessage{Helo: ss.c.Hostname(), From: ss.from, To: ss.to, Data: b, TLS: isTLS})
	ss.s.mu.Unlock()
	select {
	case ss.s.notify <- struct{}{}:
	default:
	}
	return nil
}

// Resolver answers DNS lookups from maps; names not in them do not exist.
// Names in Fail answer with a temporary error. It satisfies the resolver
// interfaces of the queue and of package auth. Set the maps before use;
// they are only read.
type Resolver struct {
	MX  map[string][]*net.MX
	IP  map[string][]net.IPAddr
	TXT map[string][]string
	// PTR maps an IP address (as net.IP.String) to host names.
	PTR  map[string][]string
	Fail map[string]bool
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (r *Resolver) fail(name string) error {
	if r.Fail[name] {
		return &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	return nil
}

func (r *Resolver) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if err := r.fail(name); err != nil {
		return nil, err
	}
	if mx, ok := r.MX[name]; ok {
		return mx, nil
	}
	return nil, notFound(name)
}

func (r *Resolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if err := r.fail(host); err != nil {
		return nil, err
	}
	if ip, ok := r.IP[host]; ok {
		return ip, nil
	}
	return nil, notFound(host)
}

func (r *Resolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if err := r.fail(name); err != nil {
		return nil, err
	}
	if t, ok := r.TXT[name]; ok {
		return t, nil
	}
	return nil, notFound(name)
}

func (r *Resolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	if err := r.fail(addr); err != nil {
		return nil, err
	}
	if n, ok := r.PTR[addr]; ok {
		return n, nil
	}
	return nil, notFound(addr)
}

// Localhost is the address list of 127.0.0.1.
var Localhost = []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}
