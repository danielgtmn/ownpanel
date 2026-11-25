package transfer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/netcheck"
)

// Error is a failure with the HTTP status the admin socket answers and a
// text for people (never a password).
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func errorf(status int, format string, args ...any) *Error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, args...)}
}

// remoteErr is a failure of the other server (502).
func remoteErr(format string, args ...any) *Error {
	return errorf(http.StatusBadGateway, format, args...)
}

// Bounds of an IMAP source.
const (
	lookupTimeout = 30 * time.Second
	dialTimeout   = 30 * time.Second
	maxCredential = 1024
)

// validateSource checks an IMAP source and returns it with the host name
// in lower case.
func validateSource(src config.ImportSource) (config.ImportSource, error) {
	src.Host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(src.Host), "."))
	if !validHost(src.Host) {
		return src, errorf(http.StatusBadRequest, "the server name is not valid")
	}
	if src.Port != 993 && src.Port != 143 {
		return src, errorf(http.StatusBadRequest, "the port must be 993 (TLS) or 143 (STARTTLS)")
	}
	if src.Username == "" || len(src.Username) > maxCredential || strings.ContainsAny(src.Username, "\x00\r\n") {
		return src, errorf(http.StatusBadRequest, "the user name is missing or not valid")
	}
	if src.Password == "" || len(src.Password) > maxCredential || strings.ContainsAny(src.Password, "\x00\r\n") {
		return src, errorf(http.StatusBadRequest, "the password is missing or not valid")
	}
	return src, nil
}

// validHost accepts an IP address or a DNS name of letters, digits and
// hyphens (internationalised names in their ASCII form).
func validHost(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	if h == "" || len(h) > 253 {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// session is a signed-in connection to another IMAP server.
type session struct {
	c        *imapclient.Client
	stop     func() bool
	host     string
	password string
}

func (s *session) close() {
	s.stop()
	s.c.Close()
}

// clean turns an error of the other server into one line of at most 200
// characters without the password.
func (s *session) clean(err error) string {
	return cleanText(err.Error(), s.password)
}

func cleanText(msg, secret string) string {
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "***")
		if q := strconv.Quote(secret); len(q) > 2 {
			msg = strings.ReplaceAll(msg, q[1:len(q)-1], "***")
		}
	}
	msg = strings.Join(strings.Fields(msg), " ")
	msg = strings.TrimPrefix(msg, "imap: ")
	return shorten(msg, 200)
}

// connect resolves the host once, refuses non-public addresses, dials the
// address itself (no second lookup), speaks TLS (993) or STARTTLS (143)
// with the host name verified, and signs in. The first failed sign-in
// ends it: there is no retry. ctx ends the session at any time.
func (m *Manager) connect(ctx context.Context, src config.ImportSource) (*session, error) {
	ips, err := m.lookup(ctx, src.Host)
	if err != nil {
		return nil, err
	}
	var conn net.Conn
	var lastErr error
	for _, ip := range ips {
		dctx, cancel := context.WithTimeout(ctx, dialTimeout)
		conn, lastErr = m.opts.Dial(dctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(src.Port)))
		cancel()
		if lastErr == nil {
			break
		}
	}
	if conn == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, remoteErr("could not connect to %s:%d", src.Host, src.Port)
	}
	tlsConf := &tls.Config{ServerName: src.Host, RootCAs: m.opts.RootCAs, MinVersion: tls.VersionTLS12}
	// The library's debug output is never set: it would log LOGIN.
	opts := &imapclient.Options{}
	var c *imapclient.Client
	stopConn := context.AfterFunc(ctx, func() { conn.Close() })
	if src.Port == 993 {
		tc := tls.Client(conn, tlsConf)
		hctx, cancel := context.WithTimeout(ctx, dialTimeout)
		err := tc.HandshakeContext(hctx)
		cancel()
		if err != nil {
			stopConn()
			conn.Close()
			return nil, m.tlsFailure(ctx, src, err)
		}
		c = imapclient.New(tc, opts)
	} else {
		opts.TLSConfig = tlsConf
		c, err = imapclient.NewStartTLS(conn, opts)
		if err != nil {
			stopConn()
			conn.Close()
			return nil, m.tlsFailure(ctx, src, err)
		}
	}
	s := &session{c: c, host: src.Host, password: src.Password}
	s.stop = context.AfterFunc(ctx, func() { c.Close() })
	stopConn()
	if err := c.WaitGreeting(); err != nil {
		s.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, remoteErr("%s did not greet: %s", src.Host, s.clean(err))
	}
	if err := c.Login(src.Username, src.Password).Wait(); err != nil {
		s.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ie *imap.Error
		if errors.As(err, &ie) {
			return nil, remoteErr("signing in to %s failed: %s", src.Host, s.clean(err))
		}
		return nil, remoteErr("signing in to %s failed: the connection was lost", src.Host)
	}
	return s, nil
}

func (m *Manager) tlsFailure(ctx context.Context, src config.ImportSource, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ce *tls.CertificateVerificationError
	if errors.As(err, &ce) {
		return remoteErr("the certificate of %s is not valid for this name", src.Host)
	}
	if src.Port == 143 {
		return remoteErr("%s does not offer encryption (STARTTLS) on port 143: %s", src.Host, cleanText(err.Error(), src.Password))
	}
	return remoteErr("no secure connection to %s:%d: %s", src.Host, src.Port, cleanText(err.Error(), src.Password))
}

// lookup resolves the host once; every address must be public (unless
// private addresses are allowed for test networks).
func (m *Manager) lookup(ctx context.Context, host string) ([]net.IP, error) {
	var addrs []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		addrs = []net.IPAddr{{IP: ip}}
	} else {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		defer cancel()
		var err error
		addrs, err = m.opts.Resolver.LookupIPAddr(lctx, host)
		if err != nil || len(addrs) == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, remoteErr("the address of %s could not be found", host)
		}
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if !m.opts.AllowPrivate && !netcheck.Public(a.IP) {
			return nil, errorf(http.StatusBadRequest, "%s points to a private or local address; only servers on the internet can be imported from", host)
		}
		ips = append(ips, a.IP)
	}
	if len(ips) > 5 {
		ips = ips[:5]
	}
	return ips, nil
}

// downloadLimited tells whether the other server stopped us for its
// download limit (Gmail: "[ALERT] ... bandwidth limits").
func downloadLimited(err error) bool {
	var ie *imap.Error
	if !errors.As(err, &ie) {
		return false
	}
	t := strings.ToLower(ie.Text)
	return ie.Code == imap.ResponseCodeAlert || strings.Contains(t, "bandwidth") ||
		strings.Contains(t, "download limit") || strings.Contains(t, "exceeded")
}

// connectionLost tells whether err means the connection broke (not an
// answer of the server).
func connectionLost(err error) bool {
	var ie *imap.Error
	return err != nil && !errors.As(err, &ie)
}
