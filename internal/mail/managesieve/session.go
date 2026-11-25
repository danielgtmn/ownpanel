package managesieve

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

// Limits of a session.
const (
	// preAuthTimeout bounds each command before sign-in, authTimeout
	// after it (the time to send one command, literals included).
	preAuthTimeout = 30 * time.Second
	authTimeout    = 5 * time.Minute
	writeTimeout   = time.Minute
	// handshakeTimeout bounds the TLS handshake after STARTTLS.
	handshakeTimeout = 30 * time.Second
	// maxFailures: failed sign-ins per connection before it is closed.
	maxFailures = 3
	// maxPreAuthCommands bounds the commands before sign-in.
	maxPreAuthCommands = 20
	// maxRedirects is announced as MAXREDIRECTS.
	maxRedirects = 5
)

// session is one connection. Its goroutine is the only reader; writes
// (answers, and BYE from Reconfigure or Shutdown) take wmu.
type session struct {
	srv *Server
	ip  netip.Addr

	mu     sync.Mutex
	c      net.Conn
	g      signin.Grant
	authed bool
	ended  bool

	wmu sync.Mutex
	w   *bufio.Writer

	p        parser
	tls      bool
	commands int
	failures int
	ctx      context.Context
}

func newSession(srv *Server, c net.Conn) *session {
	ap, _ := netip.ParseAddrPort(c.RemoteAddr().String())
	s := &session{srv: srv, ip: ap.Addr().Unmap(), c: c, ctx: context.Background()}
	s.p.r = bufio.NewReaderSize(c, maxLine)
	s.w = bufio.NewWriter(c)
	return s
}

func (s *session) conn() net.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c
}

func (s *session) grant() (signin.Grant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.g, s.authed
}

// errEnd ends the session after its answer is written.
var errEnd = errors.New("end of session")

func (s *session) serve() {
	defer s.conn().Close()
	if s.writeCaps("OK", "", "ownpanel ManageSieve ready") != nil {
		return
	}
	for {
		timeout := preAuthTimeout
		if _, ok := s.grant(); ok {
			timeout = authTimeout
		}
		s.conn().SetReadDeadline(time.Now().Add(timeout))
		cmd, err := s.p.readCommand()
		var se *syntaxError
		switch {
		case errors.As(err, &se):
			if s.count() != nil || s.reply("NO", "", se.msg) != nil {
				return
			}
			continue
		case errors.Is(err, errLineTooLong):
			s.bye("", "Line too long")
			return
		case errors.Is(err, errLiteralTooLong):
			s.bye("QUOTA/MAXSIZE", "Script too large")
			return
		case err != nil:
			return // closed, timed out or broken
		}
		if cmd.name == "" {
			continue
		}
		if s.count() != nil {
			return
		}
		if err := s.dispatch(cmd); err != nil {
			return
		}
	}
}

// count counts commands before sign-in and ends the session past the
// limit.
func (s *session) count() error {
	if _, ok := s.grant(); ok {
		return nil
	}
	if s.commands++; s.commands > maxPreAuthCommands {
		s.bye("", "Too many commands without signing in")
		return errEnd
	}
	return nil
}

// dispatch runs one command; an error ends the session.
func (s *session) dispatch(cmd command) error {
	switch cmd.name {
	case "CAPABILITY":
		if len(cmd.args) != 0 {
			return s.syntax()
		}
		return s.writeCaps("OK", "", "Done")
	case "NOOP":
		if len(cmd.args) > 1 || len(cmd.args) == 1 && (cmd.args[0].isNum || cmd.args[0].tooBig) {
			return s.syntax()
		}
		if len(cmd.args) == 1 {
			return s.reply("OK", "TAG "+quote(cmd.args[0].str), "Done")
		}
		return s.reply("OK", "", "Done")
	case "LOGOUT":
		s.reply("OK", "", "Logout")
		return errEnd
	case "STARTTLS":
		if len(cmd.args) != 0 {
			return s.syntax()
		}
		return s.startTLS()
	}
	if !s.tls {
		return s.reply("NO", "ENCRYPT-NEEDED", "Use STARTTLS first")
	}
	if cmd.name == "AUTHENTICATE" {
		return s.authenticate(cmd)
	}
	g, ok := s.grant()
	if !ok {
		if isKnown(cmd.name) {
			return s.reply("NO", "", "Sign in first")
		}
		return s.reply("NO", "", "Unknown command")
	}
	// A password change, a revoked app password or a removed mailbox ends
	// the session before anything else happens.
	if !signin.Valid(s.srv.opts.Directory(), g) {
		s.bye("", "Sign-in no longer valid")
		return errEnd
	}
	if cmd.tooBig() {
		return s.reply("NO", "QUOTA/MAXSIZE", "Script too large")
	}
	return s.signedIn(g.StorageID, cmd)
}

func isKnown(name string) bool {
	switch name {
	case "HAVESPACE", "PUTSCRIPT", "LISTSCRIPTS", "SETACTIVE", "GETSCRIPT",
		"DELETESCRIPT", "RENAMESCRIPT", "CHECKSCRIPT":
		return true
	}
	return false
}

// strs returns the arguments as strings if there are n and all are
// strings.
func strs(cmd command, n int) ([]string, bool) {
	if len(cmd.args) != n {
		return nil, false
	}
	out := make([]string, n)
	for i, a := range cmd.args {
		if a.isNum {
			return nil, false
		}
		out[i] = a.str
	}
	return out, true
}

func (s *session) signedIn(id string, cmd command) error {
	st := s.srv.opts.Store
	switch cmd.name {
	case "LISTSCRIPTS":
		if len(cmd.args) != 0 {
			return s.syntax()
		}
		list, err := st.SieveScripts(s.ctx, id)
		if err != nil {
			return s.fail(err)
		}
		var b strings.Builder
		for _, sc := range list {
			b.WriteString(quote(sc.Name))
			if sc.Active {
				b.WriteString(" ACTIVE")
			}
			b.WriteString("\r\n")
		}
		return s.raw(b.String(), "OK", "", "Done")

	case "GETSCRIPT":
		a, ok := strs(cmd, 1)
		if !ok {
			return s.syntax()
		}
		content, _, err := st.SieveScript(s.ctx, id, a[0])
		if err != nil {
			return s.fail(err)
		}
		return s.raw(literal(content)+"\r\n", "OK", "", "Done")

	case "HAVESPACE":
		if len(cmd.args) != 2 || cmd.args[0].isNum || !cmd.args[1].isNum {
			return s.syntax()
		}
		name, size := cmd.args[0].str, cmd.args[1].num
		if !sieve.ValidScriptName(name) {
			return s.reply("NO", "", "Invalid script name")
		}
		if size > sieve.MaxScriptBytes {
			return s.reply("NO", "QUOTA/MAXSIZE", "Scripts can be at most "+strconv.Itoa(sieve.MaxScriptBytes>>10)+" KiB")
		}
		full, err := s.full(id, name)
		if err != nil {
			return s.fail(err)
		}
		if full {
			return s.fail(storage.ErrTooManyScript)
		}
		return s.reply("OK", "", "Done")

	case "PUTSCRIPT":
		a, ok := strs(cmd, 2)
		if !ok {
			return s.syntax()
		}
		if !sieve.ValidScriptName(a[0]) {
			return s.reply("NO", "", "Invalid script name")
		}
		if ok, err := s.check(a[1]); !ok {
			return err
		}
		if err := st.PutSieveScript(s.ctx, id, a[0], a[1], sieve.MaxScripts, false); err != nil {
			return s.fail(err)
		}
		return s.reply("OK", "", "Script stored")

	case "CHECKSCRIPT":
		a, ok := strs(cmd, 1)
		if !ok {
			return s.syntax()
		}
		if ok, err := s.check(a[0]); !ok {
			return err
		}
		return s.reply("OK", "", "Script is valid")

	case "SETACTIVE":
		a, ok := strs(cmd, 1)
		if !ok {
			return s.syntax()
		}
		if err := st.SetActiveSieveScript(s.ctx, id, a[0]); err != nil {
			return s.fail(err)
		}
		return s.reply("OK", "", "Done")

	case "DELETESCRIPT":
		a, ok := strs(cmd, 1)
		if !ok {
			return s.syntax()
		}
		if err := st.DeleteSieveScript(s.ctx, id, a[0]); err != nil {
			return s.fail(err)
		}
		return s.reply("OK", "", "Script deleted")

	case "RENAMESCRIPT":
		a, ok := strs(cmd, 2)
		if !ok {
			return s.syntax()
		}
		if !sieve.ValidScriptName(a[1]) {
			return s.reply("NO", "", "Invalid script name")
		}
		if err := st.RenameSieveScript(s.ctx, id, a[0], a[1]); err != nil {
			return s.fail(err)
		}
		return s.reply("OK", "", "Script renamed")
	}
	return s.reply("NO", "", "Unknown command")
}

// full tells whether a new script called name would be one too many.
func (s *session) full(id, name string) (bool, error) {
	list, err := s.srv.opts.Store.SieveScripts(s.ctx, id)
	if err != nil {
		return false, err
	}
	for _, sc := range list {
		if sc.Name == name {
			return false, nil
		}
	}
	return len(list) >= sieve.MaxScripts, nil
}

// check compiles a script; if it is not valid it answers NO with the
// problem and returns false (and the write error).
func (s *session) check(src string) (bool, error) {
	if len(src) > sieve.MaxScriptBytes {
		return false, s.reply("NO", "QUOTA/MAXSIZE", "Script too large")
	}
	if _, err := sieve.Compile(src); err != nil {
		msg := "The script is not valid"
		var se *sieve.Error
		if errors.As(err, &se) {
			msg = se.Error()
		}
		return false, s.reply("NO", "", msg)
	}
	return true, nil
}

// fail answers a storage error; nil when err is nil.
func (s *session) fail(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNoScript):
		return s.reply("NO", "NONEXISTENT", "No such script")
	case errors.Is(err, storage.ErrScriptExists):
		return s.reply("NO", "ALREADYEXISTS", "A script of that name exists")
	case errors.Is(err, storage.ErrScriptActive):
		return s.reply("NO", "ACTIVE", "The active script cannot be deleted")
	case errors.Is(err, storage.ErrTooManyScript):
		return s.reply("NO", "QUOTA/MAXSCRIPTS", "A mailbox can have at most "+strconv.Itoa(sieve.MaxScripts)+" scripts")
	}
	s.srv.log.Error("managesieve: storage", "err", err)
	return s.reply("NO", "TRYLATER", "Internal error, try again later")
}

func (s *session) syntax() error { return s.reply("NO", "", "Syntax error") }

// startTLS switches to TLS and sends the capabilities again (RFC 5804
// 2.2). Commands the client sent after STARTTLS without waiting would be
// read as if they came over TLS; the connection is closed instead.
func (s *session) startTLS() error {
	if s.tls {
		return s.reply("NO", "", "TLS is active already")
	}
	if err := s.reply("OK", "", "Begin TLS negotiation now"); err != nil {
		return err
	}
	if s.p.r.Buffered() > 0 {
		return errEnd
	}
	raw := s.conn()
	tc := tls.Server(raw, s.srv.tls)
	raw.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := tc.Handshake(); err != nil {
		return err
	}
	raw.SetDeadline(time.Time{})
	s.mu.Lock()
	s.c = tc
	ended := s.ended
	s.mu.Unlock()
	if ended {
		return errEnd
	}
	s.tls = true
	s.p.r = bufio.NewReaderSize(tc, maxLine)
	s.wmu.Lock()
	s.w = bufio.NewWriter(tc)
	s.wmu.Unlock()
	return s.writeCaps("OK", "", "TLS active")
}

// authenticate signs in with PLAIN: authorization identity (empty or the
// user), user and password.
func (s *session) authenticate(cmd command) error {
	if _, ok := s.grant(); ok {
		return s.reply("NO", "", "Already signed in")
	}
	if len(cmd.args) < 1 || len(cmd.args) > 2 || cmd.args[0].isNum || (len(cmd.args) == 2 && (cmd.args[1].isNum || cmd.args[1].tooBig)) {
		return s.syntax()
	}
	if !strings.EqualFold(cmd.args[0].str, "PLAIN") {
		return s.reply("NO", "", "Unsupported sign-in mechanism")
	}
	var resp string
	if len(cmd.args) == 2 {
		resp = cmd.args[1].str
	} else {
		if err := s.write(`""` + "\r\n"); err != nil {
			return err
		}
		r, abort, err := s.p.readResponse()
		var se *syntaxError
		switch {
		case errors.As(err, &se):
			return s.reply("NO", "", se.msg)
		case err != nil:
			return err
		case abort:
			return s.reply("NO", "", "Sign-in cancelled")
		}
		resp = r
	}
	dec, err := base64.StdEncoding.DecodeString(resp)
	if err != nil {
		return s.authFailed("Invalid sign-in data")
	}
	parts := bytes.Split(dec, []byte{0})
	if len(parts) != 3 {
		return s.authFailed("Invalid sign-in data")
	}
	authz, user, pass := string(parts[0]), string(parts[1]), string(parts[2])
	if authz != "" && !strings.EqualFold(authz, user) {
		return s.authFailed("Signing in as someone else is not possible")
	}
	g, err := s.srv.opts.SignIn.Check(s.srv.opts.Directory(), s.ip, user, pass)
	if errors.Is(err, signin.ErrThrottled) {
		return s.reply("NO", "TRYLATER", "Too many failed sign-ins, try again later")
	}
	if err != nil {
		return s.authFailed("Wrong address or password")
	}
	if err := s.srv.opts.Store.EnsureMailbox(s.ctx, g.StorageID); err != nil {
		return s.fail(err)
	}
	s.mu.Lock()
	s.g, s.authed = g, true
	s.mu.Unlock()
	if s.srv.opts.SignedIn != nil {
		s.srv.opts.SignedIn(s.ctx, g.Mailbox, s.ip)
	}
	return s.reply("OK", "", "Signed in")
}

func (s *session) authFailed(msg string) error {
	if s.failures++; s.failures >= maxFailures {
		s.bye("", msg)
		return errEnd
	}
	return s.reply("NO", "", msg)
}

// --- Answers ---

func (s *session) capabilities() string {
	var b strings.Builder
	b.WriteString(`"IMPLEMENTATION" "ownpanel"` + "\r\n")
	if s.tls {
		b.WriteString(`"SASL" "PLAIN"` + "\r\n")
	} else {
		b.WriteString(`"SASL" ""` + "\r\n")
		b.WriteString(`"STARTTLS"` + "\r\n")
	}
	b.WriteString(`"SIEVE" ` + quote(strings.Join(sieve.Extensions, " ")) + "\r\n")
	b.WriteString(`"MAXREDIRECTS" "` + strconv.Itoa(maxRedirects) + `"` + "\r\n")
	b.WriteString(`"VERSION" "1.0"` + "\r\n")
	return b.String()
}

func (s *session) writeCaps(kind, code, text string) error {
	return s.raw(s.capabilities(), kind, code, text)
}

func response(kind, code, text string) string {
	line := kind
	if code != "" {
		line += " (" + code + ")"
	}
	if text != "" {
		line += " " + quote(text)
	}
	return line + "\r\n"
}

func (s *session) reply(kind, code, text string) error {
	return s.write(response(kind, code, text))
}

// raw writes data lines followed by a response.
func (s *session) raw(data, kind, code, text string) error {
	return s.write(data + response(kind, code, text))
}

func (s *session) write(data string) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	ended, c := s.ended, s.c
	s.mu.Unlock()
	if ended {
		return errEnd
	}
	c.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := s.w.WriteString(data); err != nil {
		return err
	}
	return s.w.Flush()
}

// bye says BYE and closes the connection; the session's goroutine then
// fails on its next read. Safe to call from other goroutines.
func (s *session) bye(code, text string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	c := s.c
	s.mu.Unlock()
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	s.w.WriteString(response("BYE", code, text))
	s.w.Flush()
	c.Close()
}
