package queue

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// errTLS marks a failed STARTTLS: the host is tried again without TLS.
type errTLS struct{ err error }

func (e *errTLS) Error() string { return "TLS handshake failed: " + e.err.Error() }
func (e *errTLS) Unwrap() error { return e.err }

// tryAddr delivers to one address of a receiving server. A nil error means
// the server took part in the transaction and results has one entry per
// recipient; an error means the next host or address should be tried
// (unless classify finds it permanent).
func (q *Queue) tryAddr(ctx context.Context, host string, ip net.IP, m storage.QueuedMessage, rcpts []storage.QueuedRcpt) ([]result, error) {
	ctx, cancel := context.WithTimeout(ctx, connLimit)
	defer cancel()
	c, err := q.connect(ctx, host, ip, true)
	var te *errTLS
	if errors.As(err, &te) {
		q.log.Info("mail queue: TLS failed, trying without", "host", host, "err", te.err)
		c, err = q.connect(ctx, host, ip, false)
	}
	if err != nil {
		return nil, err
	}
	defer c.Close()

	results := make([]result, len(rcpts))
	if err := c.Mail(m.Sender, &smtp.MailOptions{Size: m.Size}); err != nil {
		var se *smtp.SMTPError
		if errors.As(err, &se) && se.Code >= 500 {
			r := classify(err)
			for i := range results {
				results[i] = r
			}
			c.Quit()
			return results, nil
		}
		return nil, err
	}
	var accepted []int
	for i, r := range rcpts {
		if err := c.Rcpt(r.Address, nil); err != nil {
			results[i] = classify(err)
			continue
		}
		accepted = append(accepted, i)
	}
	if len(accepted) == 0 {
		c.Quit()
		return results, nil
	}
	setAccepted := func(r result) {
		for _, i := range accepted {
			results[i] = r
		}
	}
	w, err := c.Data()
	if err != nil {
		setAccepted(classify(err))
		return results, nil
	}
	f, err := q.opts.Store.OpenQueued(m)
	if err != nil {
		w.Close()
		setAccepted(result{outcome: temporary, status: "4.3.0", diag: "local error reading the message"})
		q.log.Error("mail queue: open message", "err", err)
		return results, nil
	}
	_, err = io.Copy(w, f)
	f.Close()
	if err != nil {
		setAccepted(classify(err))
		return results, nil
	}
	if err := w.Close(); err != nil {
		setAccepted(classify(err))
		return results, nil
	}
	setAccepted(result{outcome: delivered, status: "2.0.0"})
	c.Quit()
	return results, nil
}

// connect opens an SMTP session with host at ip: greeting, EHLO with our
// host name and, when useTLS and the server offers it, STARTTLS. The
// plain-text part is done here because the library greets as "localhost"
// before STARTTLS, which receivers hold against the sender.
func (q *Queue) connect(ctx context.Context, host string, ip net.IP, useTLS bool) (*smtp.Client, error) {
	network := "tcp4"
	if ip.To4() == nil {
		network = "tcp6"
	}
	conn, err := q.opts.Dial(ctx, network, net.JoinHostPort(ip.String(), q.opts.Port))
	if err != nil {
		return nil, err
	}
	context.AfterFunc(ctx, func() { conn.Close() })
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	hostname := q.opts.Directory().Hostname()
	conn.SetDeadline(time.Now().Add(q.opts.Timeout))
	br := bufio.NewReader(conn)
	tp := textproto.NewReader(br)
	if _, _, err := tp.ReadResponse(220); err != nil {
		return nil, protoErr(err)
	}
	if _, err := fmt.Fprintf(conn, "EHLO %s\r\n", hostname); err != nil {
		return nil, err
	}
	_, msg, err := tp.ReadResponse(250)
	offered := false
	if err == nil {
		for _, l := range strings.Split(msg, "\n")[1:] {
			if f := strings.Fields(l); len(f) > 0 && strings.EqualFold(f[0], "STARTTLS") {
				offered = true
			}
		}
	} else if _, ok := err.(*textproto.Error); !ok {
		return nil, err
	}
	var src io.Reader = br
	var raw net.Conn = conn
	if useTLS && offered {
		if _, err := io.WriteString(conn, "STARTTLS\r\n"); err != nil {
			return nil, err
		}
		if _, _, err := tp.ReadResponse(220); err != nil {
			if _, ok := err.(*textproto.Error); ok {
				return nil, &errTLS{err}
			}
			return nil, err
		}
		if br.Buffered() > 0 {
			return nil, errors.New("server sent data before the TLS handshake")
		}
		tc := tls.Client(conn, &tls.Config{
			ServerName: host,
			// Opportunistic TLS (RFC 7435): an unverifiable certificate is
			// still better than plain text. MTA-STS comes later.
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		})
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, &errTLS{err}
		}
		src, raw = tc, tc
	}
	conn.SetDeadline(time.Time{})
	// The library expects a greeting; the server greeted already, so it
	// gets a copy. It then says EHLO again (with our name), as RFC 3207
	// requires after STARTTLS and RFC 5321 allows otherwise.
	c := smtp.NewClient(&greetedConn{Conn: raw, r: io.MultiReader(strings.NewReader("220 "+host+"\r\n"), src)})
	c.CommandTimeout = q.opts.Timeout
	c.SubmissionTimeout = 2 * q.opts.Timeout
	if err := c.Hello(hostname); err != nil {
		c.Close()
		return nil, err
	}
	ok = true
	return c, nil
}

// greetedConn reads from r (a replayed greeting, then the connection).
type greetedConn struct {
	net.Conn
	r io.Reader
}

func (c *greetedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// protoErr turns a textproto reply error into an *smtp.SMTPError.
func protoErr(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		se := &smtp.SMTPError{Code: te.Code, Message: te.Msg}
		if f := strings.Fields(te.Msg); len(f) > 0 {
			var a, b, c int
			if n, _ := fmt.Sscanf(f[0], "%d.%d.%d", &a, &b, &c); n == 3 {
				se.EnhancedCode = smtp.EnhancedCode{a, b, c}
				se.Message = strings.TrimSpace(strings.TrimPrefix(te.Msg, f[0]))
			}
		}
		return se
	}
	return err
}

// classify maps an error of a delivery attempt to a result.
func classify(err error) result {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		diag := fmt.Sprintf("%d", se.Code)
		status := ""
		if se.EnhancedCode != smtp.NoEnhancedCode && se.EnhancedCode != (smtp.EnhancedCode{}) {
			status = fmt.Sprintf("%d.%d.%d", se.EnhancedCode[0], se.EnhancedCode[1], se.EnhancedCode[2])
			diag += " " + status
		}
		diag += " " + oneLine(se.Message)
		if se.Code >= 500 {
			if status == "" || status[0] != '5' {
				status = "5.0.0"
			}
			return result{outcome: permanent, status: status, diag: diag}
		}
		if status == "" || status[0] != '4' {
			status = "4.0.0"
		}
		return result{outcome: temporary, status: status, diag: diag}
	}
	var te *errTLS
	switch {
	case errors.As(err, &te):
		return result{outcome: temporary, status: "4.7.0", diag: te.Error()}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return result{outcome: temporary, status: "4.4.7", diag: "connection timed out"}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return result{outcome: temporary, status: "4.4.2", diag: "connection timed out"}
	}
	return result{outcome: temporary, status: "4.4.1", diag: "connection failed: " + oneLine(err.Error())}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}
