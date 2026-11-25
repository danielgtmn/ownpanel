package managesieve

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/sieve"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

const (
	alice    = "alice@example.org"
	password = "correct horse"
)

var idAlice = mailtest.ID('a')

type env struct {
	t        *testing.T
	store    *storage.Store
	srv      *Server
	addr     string
	cfg      *config.Config
	dir      atomic.Pointer[directory.Directory]
	appToken string
	// signIns counts what the sending limiter was told.
	signIns atomic.Int32
}

func start(t *testing.T) *env {
	t.Helper()
	e := &env{t: t}
	st, err := storage.Open(t.TempDir(), storage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.store = st
	key := config.NewAppPasswordKey()
	apID, secret, token := config.NewAppPassword()
	e.appToken = token
	e.cfg = &config.Config{
		Hostname:       "mail.example.net",
		AppPasswordKey: key,
		Certificates:   []config.Certificate{mailtest.Cert(t, "mail.example.net", false)},
		Domains:        []config.Domain{{Name: "example.org"}},
		Mailboxes: []config.Mailbox{{StorageID: idAlice, Address: alice, PasswordHash: mailtest.CheapHash(password),
			AppPasswords: []config.AppPassword{{ID: apID, Name: "Phone", MAC: config.AppPasswordMAC(key, idAlice, apID, secret)}}}},
	}
	e.dir.Store(directory.New(e.cfg))
	kp, err := tls.X509KeyPair([]byte(e.cfg.Certificates[0].CertPEM), []byte(e.cfg.Certificates[0].KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	e.srv = New(Options{
		Store:     st,
		Directory: e.dir.Load,
		SignIn:    signin.New(signin.Options{}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{kp}},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		SignedIn: func(_ context.Context, b config.Mailbox, ip netip.Addr) {
			if b.StorageID == idAlice && ip.IsLoopback() {
				e.signIns.Add(1)
			}
		},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.addr = ln.Addr().String()
	done := make(chan error, 1)
	go func() { done <- e.srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e.srv.Shutdown(ctx)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return e
}

type client struct {
	t *testing.T
	c net.Conn
	r *bufio.Reader
}

func (e *env) dial() *client {
	e.t.Helper()
	c, err := net.Dial("tcp", e.addr)
	if err != nil {
		e.t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	e.t.Cleanup(func() { c.Close() })
	cl := &client{t: e.t, c: c, r: bufio.NewReader(c)}
	if _, final := cl.resp(); !strings.HasPrefix(final, "OK") {
		e.t.Fatalf("greeting %q", final)
	}
	return cl
}

func (c *client) send(s string) {
	c.t.Helper()
	if _, err := io.WriteString(c.c, s+"\r\n"); err != nil {
		c.t.Fatal(err)
	}
}

// resp reads data lines (literals folded into them) up to the final
// OK, NO or BYE line.
func (c *client) resp() (data []string, final string) {
	c.t.Helper()
	for {
		line, err := c.line()
		if err != nil {
			c.t.Fatalf("read: %v (after %q)", err, data)
		}
		if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "NO") || strings.HasPrefix(line, "BYE") {
			return data, line
		}
		data = append(data, line)
	}
}

func (c *client) line() (string, error) {
	l, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	l = strings.TrimSuffix(l, "\r\n")
	if i := strings.LastIndexByte(l, '{'); i >= 0 && strings.HasSuffix(l, "}") {
		n, err := strconv.Atoi(strings.TrimSuffix(l[i+1:len(l)-1], "+"))
		if err == nil {
			buf := make([]byte, n)
			if _, err := io.ReadFull(c.r, buf); err != nil {
				return "", err
			}
			rest, err := c.line()
			return l[:i] + string(buf) + rest, err
		}
	}
	return l, nil
}

// cmd sends a command and returns the data lines and the final line.
func (c *client) cmd(s string) ([]string, string) {
	c.t.Helper()
	c.send(s)
	return c.resp()
}

func (c *client) ok(s string) []string {
	c.t.Helper()
	data, final := c.cmd(s)
	if !strings.HasPrefix(final, "OK") {
		c.t.Fatalf("%q: %q", s, final)
	}
	return data
}

func (c *client) starttls() []string {
	c.t.Helper()
	c.ok("STARTTLS")
	tc := tls.Client(c.c, &tls.Config{InsecureSkipVerify: true, ServerName: "mail.example.net"})
	if err := tc.Handshake(); err != nil {
		c.t.Fatal(err)
	}
	c.c, c.r = tc, bufio.NewReader(tc)
	data, final := c.resp()
	if !strings.HasPrefix(final, "OK") {
		c.t.Fatalf("after STARTTLS: %q", final)
	}
	return data
}

func plain(authz, user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(authz + "\x00" + user + "\x00" + pass))
}

func (c *client) login(user, pass string) string {
	c.t.Helper()
	_, final := c.cmd(`AUTHENTICATE "PLAIN" "` + plain("", user, pass) + `"`)
	return final
}

func (e *env) signedIn() *client {
	e.t.Helper()
	c := e.dial()
	c.starttls()
	if f := c.login(alice, password); !strings.HasPrefix(f, "OK") {
		e.t.Fatalf("login: %q", f)
	}
	return c
}

func lit(s string) string { return "{" + strconv.Itoa(len(s)) + "+}\r\n" + s }

func has(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}

func TestCapabilitiesAndTLS(t *testing.T) {
	e := start(t)
	c, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	cl := &client{t: t, c: c, r: bufio.NewReader(c)}
	greet, final := cl.resp()
	if !strings.HasPrefix(final, "OK") || !has(greet, `"STARTTLS"`) || !has(greet, `"SASL" ""`) || has(greet, `"SASL" "PLAIN"`) ||
		!has(greet, `"IMPLEMENTATION" "ownpanel"`) || !has(greet, `"VERSION" "1.0"`) || !has(greet, `"MAXREDIRECTS" "5"`) ||
		!has(greet, `"SIEVE" "`+strings.Join(sieve.Extensions, " ")+`"`) {
		t.Fatalf("greeting %q %q", greet, final)
	}
	if caps := cl.ok("CAPABILITY"); !has(caps, `"STARTTLS"`) {
		t.Fatalf("caps %q", caps)
	}
	cl.ok("NOOP")
	if _, f := cl.cmd(`NOOP "abc"`); f != `OK (TAG "abc") "Done"` {
		t.Fatalf("noop tag %q", f)
	}
	// Nothing else before TLS, sign-in included.
	if f := cl.login(alice, password); !strings.HasPrefix(f, "NO (ENCRYPT-NEEDED)") {
		t.Fatalf("auth before TLS: %q", f)
	}
	if _, f := cl.cmd("LISTSCRIPTS"); !strings.HasPrefix(f, "NO (ENCRYPT-NEEDED)") {
		t.Fatalf("listscripts before TLS: %q", f)
	}
	caps := cl.starttls()
	if has(caps, `"STARTTLS"`) || !has(caps, `"SASL" "PLAIN"`) {
		t.Fatalf("caps after TLS %q", caps)
	}
	if _, f := cl.cmd("STARTTLS"); !strings.HasPrefix(f, "NO") {
		t.Fatalf("second STARTTLS %q", f)
	}
	if _, f := cl.cmd("LISTSCRIPTS"); f != `NO "Sign in first"` {
		t.Fatalf("listscripts before sign-in: %q", f)
	}
	if _, f := cl.cmd("FROB"); !strings.HasPrefix(f, "NO") {
		t.Fatalf("unknown %q", f)
	}
	if _, f := cl.cmd("LOGOUT"); !strings.HasPrefix(f, "OK") {
		t.Fatalf("logout %q", f)
	}
	if _, err := cl.r.ReadByte(); err == nil {
		t.Fatal("open after LOGOUT")
	}
}

func TestStartTLSPipelined(t *testing.T) {
	e := start(t)
	c := e.dial()
	// A command sent together with STARTTLS must not be taken as
	// encrypted.
	c.send("STARTTLS\r\nCAPABILITY")
	if _, f := c.resp(); !strings.HasPrefix(f, "OK") {
		t.Fatal(f)
	}
	if _, err := c.r.ReadByte(); err == nil {
		t.Fatal("connection kept")
	}
}

func TestAuthenticate(t *testing.T) {
	e := start(t)
	c := e.dial()
	c.starttls()
	if f := c.login(alice, "wrong"); !strings.HasPrefix(f, "NO") {
		t.Fatalf("wrong password: %q", f)
	}
	// Authorization identity of someone else.
	if _, f := c.cmd(`AUTHENTICATE "PLAIN" "` + plain("bob@example.org", alice, password) + `"`); !strings.HasPrefix(f, "NO") {
		t.Fatalf("authz: %q", f)
	}
	// Unsupported mechanism does not count.
	if _, f := c.cmd(`AUTHENTICATE "LOGIN"`); !strings.HasPrefix(f, "NO") {
		t.Fatalf("login mech: %q", f)
	}
	// Continuation, aborted by the client.
	c.send(`AUTHENTICATE "PLAIN"`)
	if l, _ := c.line(); l != `""` {
		t.Fatalf("challenge %q", l)
	}
	c.send(`"*"`)
	if _, f := c.resp(); !strings.HasPrefix(f, "NO") {
		t.Fatalf("abort: %q", f)
	}
	// Continuation with the answer (authz equal to the user).
	c.send(`AUTHENTICATE "plain"`)
	if l, _ := c.line(); l != `""` {
		t.Fatalf("challenge %q", l)
	}
	c.send(`"` + plain(alice, alice, password) + `"`)
	if _, f := c.resp(); !strings.HasPrefix(f, "OK") {
		t.Fatalf("continuation: %q", f)
	}
	c.ok("LISTSCRIPTS")
	if f := c.login(alice, password); !strings.HasPrefix(f, "NO") {
		t.Fatalf("second sign-in: %q", f)
	}
}

func TestAuthenticateFailuresClose(t *testing.T) {
	e := start(t)
	c := e.dial()
	c.starttls()
	c.login(alice, "a")
	c.login(alice, "b")
	if f := c.login(alice, "c"); !strings.HasPrefix(f, "BYE") {
		t.Fatalf("third failure: %q", f)
	}
	if _, err := c.r.ReadByte(); err == nil {
		t.Fatal("connection kept")
	}
}

func TestAppPassword(t *testing.T) {
	e := start(t)
	c := e.dial()
	c.starttls()
	// A literal as the initial response.
	if _, f := c.cmd(`AUTHENTICATE "PLAIN" ` + lit(plain("", alice, e.appToken))); !strings.HasPrefix(f, "OK") {
		t.Fatalf("app password: %q", f)
	}
	c.ok("LISTSCRIPTS")
	// Revoking it ends the session at the next command.
	cfg := *e.cfg
	cfg.Mailboxes = []config.Mailbox{cfg.Mailboxes[0]}
	cfg.Mailboxes[0].AppPasswords = nil
	e.dir.Store(directory.New(&cfg))
	if _, f := c.cmd("LISTSCRIPTS"); !strings.HasPrefix(f, "BYE") {
		t.Fatalf("after revoke: %q", f)
	}
}

func TestReconfigureEndsSessions(t *testing.T) {
	e := start(t)
	c := e.signedIn()
	cfg := *e.cfg
	cfg.Mailboxes = []config.Mailbox{cfg.Mailboxes[0]}
	cfg.Mailboxes[0].PasswordHash = mailtest.CheapHash("new")
	e.dir.Store(directory.New(&cfg))
	e.srv.Reconfigure()
	if _, f := c.resp(); !strings.HasPrefix(f, "BYE") {
		t.Fatalf("after password change: %q", f)
	}
}

const filter = "require \"fileinto\";\r\nif header :contains \"subject\" \"x\" { fileinto \"X\"; }\r\n"

func TestScripts(t *testing.T) {
	e := start(t)
	c := e.signedIn()

	if d := c.ok("LISTSCRIPTS"); len(d) != 0 {
		t.Fatalf("empty list %q", d)
	}
	c.ok(`PUTSCRIPT "work" ` + lit(filter))
	// {n} without "+" too.
	c.ok(fmt.Sprintf("PUTSCRIPT \"home\" {%d}\r\n%s", len("keep;"), "keep;"))
	// Quoted script.
	c.ok(`PUTSCRIPT "q" "keep;"`)

	// Invalid script: not stored, NO with the line.
	_, f := c.cmd(`PUTSCRIPT "bad" ` + lit("keep;\r\nfrobnicate;\r\n"))
	if !strings.HasPrefix(f, `NO "line 2:`) {
		t.Fatalf("invalid script: %q", f)
	}
	if _, f := c.cmd(`GETSCRIPT "bad"`); f != `NO (NONEXISTENT) "No such script"` {
		t.Fatalf("bad stored: %q", f)
	}
	if _, f := c.cmd(`PUTSCRIPT "a/b" "keep;"`); !strings.HasPrefix(f, "NO") {
		t.Fatalf("invalid name: %q", f)
	}

	// CHECKSCRIPT.
	c.ok(`CHECKSCRIPT ` + lit(filter))
	if _, f := c.cmd(`CHECKSCRIPT "if true {"`); !strings.HasPrefix(f, `NO "line 1:`) {
		t.Fatalf("checkscript: %q", f)
	}

	// GETSCRIPT returns a literal.
	if d := c.ok(`GETSCRIPT "work"`); len(d) != 1 || d[0] != filter {
		t.Fatalf("getscript %q", d)
	}

	// SETACTIVE and the ACTIVE marker.
	c.ok(`SETACTIVE "work"`)
	d := c.ok("LISTSCRIPTS")
	if len(d) != 3 || !has(d, `"work" ACTIVE`) || !has(d, `"home"`) || !has(d, `"q"`) {
		t.Fatalf("list %q", d)
	}
	if name, _, ok, _ := e.store.ActiveSieveScript(context.Background(), idAlice); !ok || name != "work" {
		t.Fatalf("active %q", name)
	}
	if _, f := c.cmd(`SETACTIVE "nope"`); !strings.HasPrefix(f, "NO (NONEXISTENT)") {
		t.Fatalf("setactive missing: %q", f)
	}

	// The active script cannot be deleted.
	if _, f := c.cmd(`DELETESCRIPT "work"`); !strings.HasPrefix(f, "NO (ACTIVE)") {
		t.Fatalf("delete active: %q", f)
	}
	if _, f := c.cmd(`DELETESCRIPT "nope"`); !strings.HasPrefix(f, "NO (NONEXISTENT)") {
		t.Fatalf("delete missing: %q", f)
	}

	// RENAMESCRIPT keeps it active.
	if _, f := c.cmd(`RENAMESCRIPT "work" "home"`); !strings.HasPrefix(f, "NO (ALREADYEXISTS)") {
		t.Fatalf("rename onto existing: %q", f)
	}
	if _, f := c.cmd(`RENAMESCRIPT "nope" "x"`); !strings.HasPrefix(f, "NO (NONEXISTENT)") {
		t.Fatalf("rename missing: %q", f)
	}
	c.ok(`RENAMESCRIPT "work" "office"`)
	if d := c.ok("LISTSCRIPTS"); !has(d, `"office" ACTIVE`) {
		t.Fatalf("after rename %q", d)
	}

	// "" deactivates; then deleting works.
	c.ok(`SETACTIVE ""`)
	if d := c.ok("LISTSCRIPTS"); has(d, `"office" ACTIVE`) {
		t.Fatalf("still active %q", d)
	}
	c.ok(`DELETESCRIPT "office"`)
	if d := c.ok("LISTSCRIPTS"); len(d) != 2 {
		t.Fatalf("after delete %q", d)
	}
	c.ok("LOGOUT")
}

func TestLimits(t *testing.T) {
	e := start(t)
	c := e.signedIn()

	c.ok(`HAVESPACE "x" 1000`)
	if _, f := c.cmd(fmt.Sprintf(`HAVESPACE "x" %d`, sieve.MaxScriptBytes+1)); !strings.HasPrefix(f, "NO (QUOTA/MAXSIZE)") {
		t.Fatalf("havespace size: %q", f)
	}
	if _, f := c.cmd(`HAVESPACE "x" "1"`); !strings.HasPrefix(f, "NO") {
		t.Fatalf("havespace syntax: %q", f)
	}

	// A script over the size: refused, the connection goes on.
	big := "# " + strings.Repeat("x", sieve.MaxScriptBytes) + "\r\nkeep;\r\n"
	if _, f := c.cmd(`PUTSCRIPT "big" ` + lit(big)); !strings.HasPrefix(f, "NO (QUOTA/MAXSIZE)") {
		t.Fatalf("big: %q", f)
	}
	c.ok("NOOP")

	// Ten scripts at most.
	for i := range sieve.MaxScripts {
		c.ok(fmt.Sprintf(`PUTSCRIPT "s%d" "keep;"`, i))
	}
	if _, f := c.cmd(`PUTSCRIPT "s10" "keep;"`); !strings.HasPrefix(f, "NO (QUOTA/MAXSCRIPTS)") {
		t.Fatalf("eleventh: %q", f)
	}
	if _, f := c.cmd(`HAVESPACE "s10" 10`); !strings.HasPrefix(f, "NO (QUOTA/MAXSCRIPTS)") {
		t.Fatalf("havespace count: %q", f)
	}
	// Replacing one is fine.
	c.ok(`HAVESPACE "s3" 10`)
	c.ok(`PUTSCRIPT "s3" "discard;"`)

	// A literal far too large ends the connection without being read.
	c.send(`PUTSCRIPT "huge" {100000000+}`)
	if _, f := c.resp(); !strings.HasPrefix(f, "BYE (QUOTA/MAXSIZE)") {
		t.Fatalf("huge: %q", f)
	}
}

func TestLineTooLong(t *testing.T) {
	e := start(t)
	c := e.dial()
	c.send("NOOP \"" + strings.Repeat("x", maxLine) + "\"")
	if _, f := c.resp(); !strings.HasPrefix(f, "BYE") {
		t.Fatalf("long line: %q", f)
	}
}

func TestPreAuthCommandLimit(t *testing.T) {
	e := start(t)
	c := e.dial()
	var f string
	for range maxPreAuthCommands + 1 {
		_, f = c.cmd("NOOP")
	}
	if !strings.HasPrefix(f, "BYE") {
		t.Fatalf("command limit: %q", f)
	}
}

func FuzzParseCommand(f *testing.F) {
	for _, s := range []string{
		"CAPABILITY\r\n",
		"PUTSCRIPT \"a\" {5+}\r\nkeep;\r\n",
		"PUTSCRIPT \"a\" {5}\r\nkeep;\r\n",
		"HAVESPACE \"x\" 4294967295\r\n",
		"HAVESPACE \"x\" 4294967296\r\n",
		"AUTHENTICATE \"PLAIN\" \"AGFAYg==\"\r\n",
		"NOOP \"a\\\"b\\\\\"\r\n",
		"X {99999999999}\r\n",
		"X {2}\r\n\xff\xfe\r\n",
		"\"*\"\r\n",
		"X \"unterminated\r\n",
		"A {0+}\r\n {0}\r\n \"x\"\r\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		p := parser{r: bufio.NewReaderSize(bytes.NewReader(data), maxLine)}
		for range 50 {
			cmd, err := p.readCommand()
			if err != nil {
				if _, ok := err.(*syntaxError); ok {
					continue
				}
				break
			}
			if len(cmd.args) > maxArgs {
				t.Fatalf("%d args", len(cmd.args))
			}
			for _, a := range cmd.args {
				if len(a.str) > maxLiteral {
					t.Fatalf("argument of %d bytes", len(a.str))
				}
			}
		}
		p = parser{r: bufio.NewReaderSize(bytes.NewReader(data), maxLine)}
		p.readResponse()
	})
}

// Successful sign-ins are counted by network like those of submission, so
// a mailbox signed in from too many networks is blocked.
func TestSignInsCounted(t *testing.T) {
	e := start(t)
	c := e.dial()
	c.starttls()
	if f := c.login(alice, "wrong password"); strings.HasPrefix(f, "OK") {
		t.Fatal("wrong password accepted")
	}
	if n := e.signIns.Load(); n != 0 {
		t.Fatalf("failed sign-in counted: %d", n)
	}
	e.signedIn()
	if n := e.signIns.Load(); n != 1 {
		t.Fatalf("sign-ins counted: %d", n)
	}
}
