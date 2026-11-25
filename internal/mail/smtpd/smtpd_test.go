package smtpd

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/filter"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/queue"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

var (
	idAlice = mailtest.ID('a')
	idBob   = mailtest.ID('b')
	idFull  = mailtest.ID('f')
)

type env struct {
	addr  string
	srv   *Server
	store *storage.Store
	dir   atomic.Pointer[directory.Directory]
	free  atomic.Int64

	res   *mailtest.Resolver
	sink  *mailtest.Sink
	q     *queue.Queue
	srs   *auth.SRS
	clock *clock
	// client is the address connections claim to come from ("" = real).
	client atomic.Pointer[netip.Addr]
}

// clock is a settable time for tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testConfig(t *testing.T) *config.Config {
	return &config.Config{
		Hostname:     "mail.example.net",
		Certificates: []config.Certificate{mailtest.Cert(t, "mail.example.net", false)},
		Domains:      []config.Domain{{Name: "example.org"}, {Name: "empty.test"}},
		Mailboxes: []config.Mailbox{
			{StorageID: idAlice, Address: "alice@example.org", PasswordHash: mailtest.Hash},
			{StorageID: idBob, Address: "bob@example.org", PasswordHash: mailtest.Hash, QuotaBytes: 6000},
			{StorageID: idFull, Address: "full@example.org", PasswordHash: mailtest.Hash, QuotaBytes: 10},
		},
		Aliases: []config.Alias{
			{Address: "team@example.org", Targets: []string{"alice@example.org", "bob@example.org", "friend@elsewhere.test"}},
			{Address: "out@example.org", Targets: []string{"friend@elsewhere.test"}},
			{Address: "postmaster@example.org", Targets: []string{"bob@example.org"}},
		},
		Limits: config.Limits{MaxMessageBytes: 2000, ConnectionsPerIP: 3},
	}
}

func start(t *testing.T, cfg *config.Config) *env {
	t.Helper()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// The clock runs a minute ahead: the storage stamps queued messages
	// with the real time, and they must be due for the queue.
	e := &env{clock: &clock{t: time.Now().Add(time.Minute)}}
	e.dir.Store(directory.New(cfg))
	e.free.Store(10 << 30)
	st, err := storage.Open(t.TempDir(), storage.Options{Quota: func(id string) int64 {
		if m, ok := e.dir.Load().MailboxByStorageID(id); ok {
			return m.QuotaBytes
		}
		return 0
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.store = st
	kp, err := tls.X509KeyPair([]byte(cfg.Certificates[0].CertPEM), []byte(cfg.Certificates[0].KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	e.sink = mailtest.NewSink(t, mailtest.SinkPlain)
	e.res = &mailtest.Resolver{
		MX: map[string][]*net.MX{
			"elsewhere.test": {{Host: "mx.elsewhere.test", Pref: 10}},
			"sender.test":    {{Host: "mx.elsewhere.test", Pref: 10}},
		},
		IP:   map[string][]net.IPAddr{"mx.elsewhere.test": mailtest.Localhost},
		TXT:  map[string][]string{},
		PTR:  map[string][]string{},
		Fail: map[string]bool{},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	event := func(ev config.Event) { st.AddEvent(context.Background(), ev) }
	e.srs = auth.NewSRS([]byte("0123456789abcdef0123456789abcdef"), e.clock.Now)
	e.q = queue.New(queue.Options{
		Store: st, Directory: e.dir.Load, Resolver: e.res, Port: e.sink.Port,
		Now: e.clock.Now, Event: event, Logger: logger,
		ReturnTo: func(sender string) (string, bool) {
			orig, err := e.srs.Reverse(sender)
			return orig, err == nil
		},
	})
	e.srv = New(Options{
		Store:     st,
		Directory: e.dir.Load,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{kp}},
		FreeBytes: func() (int64, error) { return e.free.Load(), nil },
		Filter:    filter.New(filter.Options{Resolver: e.res, Store: st, Now: e.clock.Now, Event: event, Logger: logger}),
		Queue:     e.q,
		SRS:       e.srs,
		Now:       e.clock.Now,
		Logger:    logger,
		ClientIP: func(a net.Addr) netip.Addr {
			if c := e.client.Load(); c != nil {
				return *c
			}
			ap, _ := netip.ParseAddrPort(a.String())
			return ap.Addr().Unmap()
		},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.addr = ln.Addr().String()
	served := make(chan error, 1)
	go func() { served <- e.srv.Serve(ln) }()
	t.Cleanup(func() {
		e.srv.Close()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
		e.q.Shutdown(context.Background())
		st.Close()
	})
	return e
}

// from makes connections claim to come from ip.
func (e *env) from(ip string) {
	a := netip.MustParseAddr(ip)
	e.client.Store(&a)
}

func dial(t *testing.T, addr string, startTLS bool) *smtp.Client {
	t.Helper()
	var c *smtp.Client
	var err error
	if startTLS {
		// Fails unless STARTTLS is offered; greets as "localhost".
		c, err = smtp.DialStartTLS(addr, &tls.Config{InsecureSkipVerify: true})
	} else {
		c, err = smtp.Dial(addr)
		if err == nil {
			err = c.Hello("sender.test")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func code(err error) int {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

func send(c *smtp.Client, from string, to []string, body string) error {
	if err := c.Mail(from, nil); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r, nil); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, body); err != nil {
		return err
	}
	return w.Close()
}

func inbox(t *testing.T, e *env, id string) []string {
	t.Helper()
	_, msgs, err := e.store.Select(context.Background(), id, storage.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		f, err := e.store.Open(m)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		out = append(out, string(b))
	}
	return out
}

const body = "From: a@sender.test\r\nSubject: hi\r\n\r\nHello.\r\n"

func TestDeliverWithStartTLS(t *testing.T) {
	e := start(t, testConfig(t))
	c := dial(t, e.addr, true)
	if err := send(c, "a@sender.test", []string{"Alice@Example.org"}, body); err != nil {
		t.Fatal(err)
	}
	msgs := inbox(t, e, idAlice)
	if len(msgs) != 1 {
		t.Fatalf("%d messages", len(msgs))
	}
	m := msgs[0]
	if !strings.HasPrefix(m, "Return-Path: <a@sender.test>\r\nReceived: from localhost ([127.0.0.1])\r\n\tby mail.example.net (ownpanel) with ESMTPS (TLS 1.3)") {
		t.Fatalf("trace header:\n%s", m)
	}
	if !strings.HasSuffix(m, body) {
		t.Fatalf("body changed:\n%q", m)
	}
}

func TestGreetingAndPlainDelivery(t *testing.T) {
	e := start(t, testConfig(t))
	conn, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(conn).ReadString('\n')
	conn.Close()
	if !strings.HasPrefix(line, "220 mail.example.net ESMTP") {
		t.Fatalf("greeting %q", line)
	}
	c := dial(t, e.addr, false)
	if ok, _ := c.Extension("AUTH"); ok {
		t.Fatal("AUTH offered on port 25")
	}
	// Null sender (bounces) is accepted; plain text is fine server to server.
	if err := send(c, "", []string{"alice@example.org"}, body); err != nil {
		t.Fatal(err)
	}
	if m := inbox(t, e, idAlice); len(m) != 1 || !strings.Contains(m[0], "Return-Path: <>\r\n") || !strings.Contains(m[0], "with ESMTP id") {
		t.Fatalf("%q", m)
	}
}

func TestRecipients(t *testing.T) {
	cfg := testConfig(t)
	cfg.PostmasterAddress = "alice@example.org"
	e := start(t, cfg)
	c := dial(t, e.addr, false)
	if err := c.Mail("x@sender.test", nil); err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]int{
		"nobody@example.org":          550,
		"someone@elsewhere.test":      550, // no relaying
		"out@example.org":             0,   // forwards outside
		"alice@example.org":           0,
		"postmaster@example.org":      0, // alias → bob
		"abuse@example.org":           0, // oldest mailbox → alice
		"postmaster@mail.example.net": 0, // → postmaster address
		"abuse@empty.test":            0, // no mailbox → postmaster address
	} {
		if got := code(c.Rcpt(addr, nil)); got != want {
			t.Errorf("%s: %d, want %d", addr, got, want)
		}
	}
	c.Reset()
	if err := send(c, "x@sender.test", []string{"postmaster@example.org", "abuse@empty.test"}, body); err != nil {
		t.Fatal(err)
	}
	if len(inbox(t, e, idBob)) != 1 || len(inbox(t, e, idAlice)) != 1 {
		t.Fatal("postmaster routing")
	}
}

func TestPostmasterUnset(t *testing.T) {
	e := start(t, testConfig(t))
	c := dial(t, e.addr, false)
	c.Mail("x@sender.test", nil)
	if got := code(c.Rcpt("postmaster@mail.example.net", nil)); got != 451 {
		t.Fatalf("got %d", got)
	}
}

func TestAliasDelivery(t *testing.T) {
	e := start(t, testConfig(t))
	c := dial(t, e.addr, false)
	// alice twice (directly and through the alias) gets one copy.
	if err := send(c, "x@sender.test", []string{"team@example.org", "alice@example.org"}, body); err != nil {
		t.Fatal(err)
	}
	if len(inbox(t, e, idAlice)) != 1 || len(inbox(t, e, idBob)) != 1 {
		t.Fatal("alias expansion")
	}
	// The external target is forwarded with an SRS sender.
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 || len(got[0].To) != 1 || got[0].To[0] != "friend@elsewhere.test" {
		t.Fatalf("forwarded %+v", got)
	}
	if orig, err := e.srs.Reverse(got[0].From); err != nil || orig != "x@sender.test" || !strings.HasSuffix(got[0].From, "@example.org") {
		t.Fatalf("envelope sender %q: %q %v", got[0].From, orig, err)
	}
	if ev, _ := e.store.Events(context.Background(), 0); len(ev) != 0 {
		t.Fatalf("events %+v", ev)
	}
}

func TestQuota(t *testing.T) {
	e := start(t, testConfig(t))
	ctx := context.Background()
	// full@ is already over its quota of 10 bytes.
	if _, err := e.store.Append(ctx, idFull, storage.Inbox, strings.NewReader("0123456789"), storage.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
	c := dial(t, e.addr, false)
	c.Mail("x@sender.test", nil)
	if got := code(c.Rcpt("full@example.org", nil)); got != 452 {
		t.Fatalf("over quota at RCPT: %d", got)
	}
	c.Reset()
	// bob has 4500 bytes: a message that does not fit is refused at DATA.
	big := body + strings.Repeat("x", 1900) + "\r\n"
	for i := 0; i < 2; i++ {
		if err := send(c, "x@sender.test", []string{"bob@example.org"}, big); err != nil {
			t.Fatal(i, err)
		}
	}
	if got := code(send(c, "x@sender.test", []string{"bob@example.org"}, big)); got != 452 {
		t.Fatalf("over quota at DATA: %d", got)
	}
	c.Reset()
	// Through the alias: alice gets it, bob's failure is recorded.
	if err := send(c, "x@sender.test", []string{"team@example.org"}, big); err != nil {
		t.Fatal(err)
	}
	ev, _ := e.store.Events(ctx, 0)
	found := false
	for _, x := range ev {
		found = found || (x.StorageID == idBob && strings.Contains(x.Detail, "full"))
	}
	if !found {
		t.Fatalf("no event for bob: %+v", ev)
	}
}

func TestSizeLimit(t *testing.T) {
	e := start(t, testConfig(t))
	c := dial(t, e.addr, false)
	if ok, p := c.Extension("SIZE"); !ok || p != "2000" {
		t.Fatalf("SIZE %v %q", ok, p)
	}
	err := send(c, "x@sender.test", []string{"alice@example.org"}, body+strings.Repeat(strings.Repeat("y", 70)+"\r\n", 40))
	if got := code(err); got != 552 {
		t.Fatalf("too large: %d %v", got, err)
	}
	if len(inbox(t, e, idAlice)) != 0 {
		t.Fatal("stored anyway")
	}
}

func TestLowDisk(t *testing.T) {
	e := start(t, testConfig(t))
	e.free.Store(100 << 20)
	c := dial(t, e.addr, false)
	c.Mail("x@sender.test", nil)
	err := c.Rcpt("alice@example.org", nil)
	var se *smtp.SMTPError
	if !errors.As(err, &se) || se.Code != 452 || se.EnhancedCode != (smtp.EnhancedCode{4, 3, 1}) {
		t.Fatalf("got %v", err)
	}
}

func TestConnectionLimit(t *testing.T) {
	e := start(t, testConfig(t))
	for i := 0; i < 3; i++ {
		dial(t, e.addr, false)
	}
	conn, err := net.Dial("tcp", e.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, _ := bufio.NewReader(conn).ReadString('\n')
	if !strings.HasPrefix(line, "421 ") {
		t.Fatalf("fourth connection: %q", line)
	}
}

func TestReconfigure(t *testing.T) {
	e := start(t, testConfig(t))
	old := dial(t, e.addr, false)
	cfg := testConfig(t)
	cfg.Hostname = "mx.example.net"
	cfg.Limits.MaxMessageBytes = 5000
	e.dir.Store(directory.New(cfg))
	e.srv.Reconfigure()
	c := dial(t, e.addr, false)
	if _, p := c.Extension("SIZE"); p != "5000" {
		t.Fatalf("new limit not applied: %q", p)
	}
	// The connection opened before keeps working.
	if err := send(old, "x@sender.test", []string{"alice@example.org"}, body); err != nil {
		t.Fatal(err)
	}
	if m := inbox(t, e, idAlice); len(m) != 1 || !strings.Contains(m[0], "by mx.example.net") {
		t.Fatal("transaction did not use the configuration of its start")
	}
}

func TestGracefulShutdown(t *testing.T) {
	e := start(t, testConfig(t))
	c := dial(t, e.addr, false)
	if err := c.Mail("x@sender.test", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("alice@example.org", nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.srv.Shutdown(context.Background()) }()
	// New connections are told to come back later.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.Dial("tcp", e.addr)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if strings.HasPrefix(line, "421 4.3.2") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no 421 during shutdown: %q", line)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The running transaction finishes.
	w, err := c.Data()
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, body)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if code(c.Mail("x@sender.test", nil)) != 421 {
		t.Fatal("new transaction during shutdown")
	}
	c.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(inbox(t, e, idAlice)) != 1 {
		t.Fatal("message lost")
	}
}
