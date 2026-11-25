package submission

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/auth"
	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/queue"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

const (
	alice    = "alice@example.org"
	bob      = "bob@example.org"
	password = "correct horse"
)

var (
	idAlice = mailtest.ID('a')
	idBob   = mailtest.ID('b')

	keyOnce   sync.Once
	dkimPEM   string
	dkimTXT   string
	cheapHash = mailtest.CheapHash(password)
)

func dkimKey(t testing.TB) (string, string) {
	keyOnce.Do(func() { dkimPEM, dkimTXT = mailtest.DKIMKey(t) })
	return dkimPEM, dkimTXT
}

type clock struct{ t atomic.Int64 }

func (c *clock) Now() time.Time      { return time.Unix(0, c.t.Load()) }
func (c *clock) Add(d time.Duration) { c.t.Add(int64(d)) }

type env struct {
	t       *testing.T
	store   *storage.Store
	dir     atomic.Pointer[directory.Directory]
	clock   *clock
	sink    *mailtest.Sink
	q       *queue.Queue
	limits  *Limiter
	addr    string // 587 style
	tlsAddr string // 465 style

	mu     sync.Mutex
	events []config.Event
}

func testConfig(t testing.TB) *config.Config {
	key, _ := dkimKey(t)
	return &config.Config{
		Hostname:     "mail.example.net",
		Certificates: []config.Certificate{mailtest.Cert(t, "mail.example.net", false)},
		Domains: []config.Domain{
			{Name: "example.org", DKIM: []config.DKIMKey{{Selector: "s1", PrivateKeyPEM: key, Active: true}}},
			{Name: "other.test"},
		},
		Mailboxes: []config.Mailbox{
			{StorageID: idAlice, Address: alice, PasswordHash: cheapHash},
			{StorageID: idBob, Address: bob, PasswordHash: cheapHash},
		},
		Aliases: []config.Alias{
			{Address: "team@example.org", Targets: []string{alice, bob}},
			{Address: "sales@example.org", Targets: []string{bob}},
			{Address: "info@other.test", Targets: []string{"team@example.org"}},
			{Address: "*@example.org", Targets: []string{alice}},
			{Address: "fwd@example.org", Targets: []string{bob, "friend@remote.test"}},
		},
		Limits: config.Limits{MaxMessageBytes: 100000, MaxRecipients: 5},
	}
}

func start(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clock: &clock{}}
	e.clock.t.Store(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).UnixNano())
	cfg := testConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	e.dir.Store(directory.New(cfg))
	st, err := storage.Open(t.TempDir(), storage.Options{Now: e.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.store = st
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	record := func(ev config.Event) {
		e.mu.Lock()
		e.events = append(e.events, ev)
		e.mu.Unlock()
	}
	e.sink = mailtest.NewSink(t, mailtest.SinkStartTLS)
	e.q = queue.New(queue.Options{
		Store:     st,
		Directory: e.dir.Load,
		Resolver: &mailtest.Resolver{
			MX: map[string][]*net.MX{"remote.test": {{Host: "mx.remote.test", Pref: 10}}},
			IP: map[string][]net.IPAddr{"mx.remote.test": mailtest.Localhost},
		},
		Port:   e.sink.Port,
		Now:    e.clock.Now,
		Event:  record,
		Logger: log,
	})
	t.Cleanup(func() { e.q.Shutdown(context.Background()) })
	e.limits = NewLimiter(st, e.clock.Now, record, log)
	kp, err := tls.X509KeyPair([]byte(cfg.Certificates[0].CertPEM), []byte(cfg.Certificates[0].KeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	checker := signin.New(signin.Options{Now: e.clock.Now})
	for _, implicit := range []bool{false, true} {
		srv := New(Options{
			Store:       st,
			Directory:   e.dir.Load,
			SignIn:      checker,
			Limits:      e.limits,
			Queue:       e.q,
			Signer:      auth.NewSigner(),
			TLSConfig:   &tls.Config{Certificates: []tls.Certificate{kp}},
			ImplicitTLS: implicit,
			Logger:      log,
			FreeBytes:   func() (int64, error) { return 10 << 30, nil },
			Now:         e.clock.Now,
		})
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if implicit {
			e.tlsAddr = ln.Addr().String()
		} else {
			e.addr = ln.Addr().String()
		}
		done := make(chan error, 1)
		go func() { done <- srv.Serve(ln) }()
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(ctx)
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	return e
}

// client connects with STARTTLS and signs in as user (PLAIN).
func (e *env) client(user string) *smtp.Client {
	e.t.Helper()
	c, err := smtp.DialStartTLS(e.addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { c.Close() })
	if err := c.Auth(sasl.NewPlainClient("", user, password)); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func message(from, to, subject string) string {
	return "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\n\r\nHello.\r\n"
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

func code(err error) int {
	var se *smtp.SMTPError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

func (e *env) inbox(id string) []string {
	e.t.Helper()
	_, msgs, err := e.store.Select(context.Background(), id, storage.Inbox)
	if errors.Is(err, storage.ErrNoFolder) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		f, err := e.store.Open(m)
		if err != nil {
			e.t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		f.Close()
		out = append(out, string(b))
	}
	return out
}

func (e *env) eventKinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var k []string
	for _, ev := range e.events {
		k = append(k, ev.Kind)
	}
	return k
}

func TestAuth(t *testing.T) {
	e := start(t)

	// PLAIN after STARTTLS.
	c := e.client(alice)
	if err := send(c, alice, []string{bob}, message(alice, bob, "plain")); err != nil {
		t.Fatal(err)
	}

	// LOGIN with the user name as initial response (go-sasl's client).
	c2, err := smtp.DialStartTLS(e.addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if err := c2.Auth(sasl.NewLoginClient(bob, password)); err != nil {
		t.Fatal("LOGIN:", err)
	}

	// Implicit TLS (465).
	c3, err := smtp.DialTLS(e.tlsAddr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	if err := c3.Auth(sasl.NewPlainClient("", alice, password)); err != nil {
		t.Fatal("465:", err)
	}
	if err := send(c3, alice, []string{bob}, message(alice, bob, "tls")); err != nil {
		t.Fatal(err)
	}
	if n := len(e.inbox(idBob)); n != 2 {
		t.Fatalf("bob has %d messages", n)
	}

	// Wrong password; another identity than the user. (go-smtp's client
	// answers a failure with "*", which the server counts as a protocol
	// error, so every attempt gets its own connection.)
	dialAuth := func(user, pw string) (*smtp.Client, error) {
		c, err := smtp.DialStartTLS(e.addr, &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c, c.Auth(sasl.NewPlainClient("", user, pw))
	}
	if _, err := dialAuth(alice, "wrong"); code(err) != 535 {
		t.Fatalf("wrong password: %v", err)
	}
	c4, err := smtp.DialStartTLS(e.addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c4.Close()
	if err := c4.Auth(sasl.NewPlainClient(bob, alice, password)); code(err) != 535 {
		t.Fatalf("other identity: %v", err)
	}
	// Without sign-in no mail.
	if err := c4.Mail(alice, nil); code(err) != 530 {
		t.Fatalf("mail without auth: %v", err)
	}
	// Throttled after the failures per (IP, address), even with the right
	// password.
	for i := 0; i < signin.PairLimit; i++ {
		dialAuth(alice, "wrong")
	}
	if _, err := dialAuth(alice, password); code(err) != 454 {
		t.Fatalf("not throttled: %v", err)
	}
}

// raw speaks SMTP by hand.
type raw struct {
	t    *testing.T
	conn net.Conn
	tp   *textproto.Conn
}

func dialRaw(t *testing.T, addr string) *raw {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	r := &raw{t: t, conn: conn, tp: textproto.NewConn(conn)}
	r.expect(220)
	return r
}

func (r *raw) cmd(want int, format string, args ...any) string {
	r.t.Helper()
	if err := r.tp.PrintfLine(format, args...); err != nil {
		r.t.Fatal(err)
	}
	return r.expect(want)
}

func (r *raw) expect(want int) string {
	r.t.Helper()
	code, msg, err := r.tp.ReadResponse(0)
	if code != want {
		r.t.Fatalf("got %d %s (%v), want %d", code, msg, err, want)
	}
	return msg
}

func TestAuthNeedsTLSOn587(t *testing.T) {
	e := start(t)
	r := dialRaw(t, e.addr)
	caps := r.cmd(250, "EHLO client.test")
	if strings.Contains(caps, "AUTH") || !strings.Contains(caps, "STARTTLS") {
		t.Fatalf("caps before TLS: %s", caps)
	}
	plain := base64.StdEncoding.EncodeToString([]byte("\x00" + alice + "\x00" + password))
	r.cmd(523, "AUTH PLAIN %s", plain)
	r.cmd(530, "MAIL FROM:<%s>", alice)

	// After STARTTLS: LOGIN step by step, without initial response.
	r.cmd(220, "STARTTLS")
	tc := tls.Client(r.conn, &tls.Config{InsecureSkipVerify: true})
	r.tp = textproto.NewConn(tc)
	caps = r.cmd(250, "EHLO client.test")
	if !strings.Contains(caps, "AUTH PLAIN LOGIN") {
		t.Fatalf("caps after TLS: %s", caps)
	}
	b64 := base64.StdEncoding.EncodeToString
	if q := r.cmd(334, "AUTH LOGIN"); q != b64([]byte("Username:")) {
		t.Fatalf("challenge %q", q)
	}
	r.cmd(334, "%s", b64([]byte(alice)))
	r.cmd(235, "%s", b64([]byte(password)))
	r.cmd(250, "MAIL FROM:<%s>", alice)
}

func TestSenderRules(t *testing.T) {
	e := start(t)
	c := e.client(alice)
	for _, tc := range []struct {
		from, header string
		want         int
	}{
		{alice, alice, 0},
		{"Alice@Example.org", "Alice <ALICE@example.org>", 0},
		{"team@example.org", "Team <team@example.org>", 0},
		// An alias through another alias.
		{"info@other.test", "info@other.test", 0},
		// Envelope fine, header From another address of hers.
		{alice, "team@example.org, alice@example.org", 0},
		// The catch-all delivers to alice but does not count.
		{"random@example.org", alice, 550},
		// An alias that does not deliver to alice.
		{"sales@example.org", alice, 550},
		{bob, bob, 550},
		{"", alice, 550},
		// Header From not hers.
		{alice, bob, 550},
		{alice, "alice@example.org, bob@example.org", 550},
		{alice, "", 550},
	} {
		body := message(tc.header, bob, "rules")
		if tc.header == "" {
			body = "To: " + bob + "\r\nSubject: no from\r\n\r\nx\r\n"
		}
		err := send(c, tc.from, []string{bob}, body)
		if code(err) != tc.want && !(tc.want == 0 && err == nil) {
			t.Errorf("from %q header %q: %v, want %d", tc.from, tc.header, err, tc.want)
		}
		c.Reset()
	}
	if n := len(e.inbox(idBob)); n != 5 {
		t.Fatalf("bob has %d messages", n)
	}

	// Blocked mailbox.
	if err := e.store.SetBlocked(context.Background(), idAlice, "test"); err != nil {
		t.Fatal(err)
	}
	err := c.Mail(alice, nil)
	if code(err) != 550 || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("blocked: %v", err)
	}
	// Receiving goes on.
	c2 := e.client(bob)
	if err := send(c2, bob, []string{alice}, message(bob, alice, "to blocked")); err != nil {
		t.Fatal(err)
	}
}

func TestSessionEndsWhenPasswordChanges(t *testing.T) {
	e := start(t)
	c := e.client(alice)
	cfg := testConfig(t)
	cfg.Mailboxes[0].PasswordHash = mailtest.CheapHash("new")
	e.dir.Store(directory.New(cfg))
	if err := c.Mail(alice, nil); code(err) != 530 {
		t.Fatalf("stale session: %v", err)
	}
}

func TestLocalAndExternalDelivery(t *testing.T) {
	e := start(t)
	c := e.client(alice)
	body := "From: Alice <alice@example.org>\nTo: team@example.org, friend@remote.test\nSubject: Hello\n\nLine one.\n.dot line\n"
	if err := send(c, alice, []string{"team@example.org", bob, "friend@remote.test", "other@remote.test", "fwd@example.org"}, body); err != nil {
		t.Fatal(err)
	}
	// Local: alice and bob once each (team, bob, fwd deduplicated).
	inA, inB := e.inbox(idAlice), e.inbox(idBob)
	if len(inA) != 1 || len(inB) != 1 {
		t.Fatalf("local %d %d", len(inA), len(inB))
	}
	m := inB[0]
	for _, want := range []string{"Return-Path: <alice@example.org>\r\n", "Received: from [127.0.0.1] (authenticated)\r\n\tby mail.example.net",
		"Message-ID: <", "Date: ", "DKIM-Signature: ", "Line one.\r\n.dot line\r\n"} {
		if !strings.Contains(m, want) {
			t.Errorf("local copy lacks %q:\n%s", want, m)
		}
	}
	// The external target of fwd@ goes out with the other external
	// recipients (deduplicated with the direct one).
	if ev, _ := e.store.Events(context.Background(), 0); len(ev) != 0 {
		t.Fatalf("events %+v", ev)
	}

	// External: one queued message for both remote recipients.
	if n, _ := e.q.Len(context.Background()); n != 1 {
		t.Fatalf("queue %d", n)
	}
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 1 || len(got[0].To) != 2 || got[0].From != alice || !got[0].TLS {
		t.Fatalf("sink %+v", got)
	}
	_, txt := dkimKey(t)
	verifications, err := dkim.VerifyWithOptions(strings.NewReader(string(got[0].Data)), &dkim.VerifyOptions{
		LookupTXT: func(domain string) ([]string, error) {
			if domain != "s1._domainkey.example.org" {
				return nil, fmt.Errorf("unexpected lookup %s", domain)
			}
			return []string{txt}, nil
		},
	})
	if err != nil || len(verifications) != 1 {
		t.Fatalf("verify: %v %d", err, len(verifications))
	}
	v := verifications[0]
	if v.Err != nil || v.Domain != "example.org" {
		t.Fatalf("signature: %v %s", v.Err, v.Domain)
	}
	signed := strings.ToLower(strings.Join(v.HeaderKeys, ":"))
	for _, h := range []string{"from", "to", "subject", "date", "message-id"} {
		if !strings.Contains(signed, h) {
			t.Errorf("%s not signed: %s", h, signed)
		}
	}
	if strings.Contains(string(got[0].Data), "Return-Path") {
		t.Error("return path sent outside")
	}

	// A domain without a key: sent unsigned.
	if err := send(c, "info@other.test", []string{"x@remote.test"}, message("info@other.test", "x@remote.test", "unsigned")); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 2 || strings.Contains(string(got[1].Data), "DKIM-Signature") {
		t.Fatal("unsigned message")
	}
}

func TestRecipients(t *testing.T) {
	e := start(t)
	c := e.client(alice)
	c.Mail(alice, nil)
	if err := c.Rcpt("nobody@other.test", nil); code(err) != 550 {
		t.Fatalf("unknown local: %v", err)
	}
	if err := c.Rcpt("bad address@remote.test", nil); err == nil {
		t.Fatal("bad address accepted")
	}
	for i := 0; i < 5; i++ {
		if err := c.Rcpt(fmt.Sprintf("r%d@remote.test", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Rcpt("r6@remote.test", nil); code(err) != 452 {
		t.Fatalf("too many recipients: %v", err)
	}
}

func TestBounceOfRemoteRefusal(t *testing.T) {
	e := start(t)
	e.sink.SetRcpt(func(string) error {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "user unknown"}
	})
	c := e.client(alice)
	if err := send(c, alice, []string{"ghost@remote.test"}, message(alice, "ghost@remote.test", "Lost")); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	in := e.inbox(idAlice)
	if len(in) != 1 || !strings.Contains(in[0], "ghost@remote.test") || !strings.Contains(in[0], "Subject: Lost") ||
		!strings.Contains(in[0], "MAILER-DAEMON@mail.example.net") {
		t.Fatalf("bounce %v", in)
	}
}

func TestRateLimitsAndBlock(t *testing.T) {
	e := start(t)
	cfg := testConfig(t)
	cfg.Mailboxes[0].SendPerHour = 1
	cfg.Mailboxes[0].SendPerDay = 10
	e.dir.Store(directory.New(cfg))
	c := e.client(alice)
	ctx := context.Background()

	hit := func() {
		t.Helper()
		if err := send(c, alice, []string{bob}, message(alice, bob, "ok")); err != nil {
			t.Fatal(err)
		}
		err := send(c, alice, []string{bob}, message(alice, bob, "over"))
		if code(err) != 451 {
			t.Fatalf("over the limit: %v", err)
		}
		c.Reset()
		// Retrying in the same hour counts once.
		send(c, alice, []string{bob}, message(alice, bob, "over"))
		c.Reset()
	}
	hit()
	if k := e.eventKinds(); len(k) != 1 || k[0] != config.EventSendLimit {
		t.Fatalf("events %v", k)
	}
	e.clock.Add(61 * time.Minute)
	hit()
	if b, _ := e.store.Blocked(ctx, idAlice); b.Blocked {
		t.Fatal("blocked after two hits")
	}
	e.clock.Add(61 * time.Minute)
	hit()
	b, _ := e.store.Blocked(ctx, idAlice)
	if !b.Blocked || !strings.Contains(b.Reason, "hourly sending limit 3 times") {
		t.Fatalf("not blocked: %+v", b)
	}
	if k := e.eventKinds(); k[len(k)-1] != config.EventBlocked {
		t.Fatalf("events %v", k)
	}
	if err := c.Mail(alice, nil); code(err) != 550 {
		t.Fatalf("blocked mail: %v", err)
	}
	// Unblocking resets the counters: she can send at once.
	if _, err := e.store.Unblock(ctx, idAlice); err != nil {
		t.Fatal(err)
	}
	if err := send(c, alice, []string{bob}, message(alice, bob, "again")); err != nil {
		t.Fatal(err)
	}
	if b, _ := e.store.Blocked(ctx, idAlice); b.Blocked {
		t.Fatal("blocked again")
	}
}

func TestDailyAndDomainLimits(t *testing.T) {
	e := start(t)
	cfg := testConfig(t)
	cfg.Mailboxes[0].SendPerDay = 2
	cfg.Domains[0].SendPerHour = 3
	e.dir.Store(directory.New(cfg))
	ca, cb := e.client(alice), e.client(bob)
	for i := 0; i < 2; i++ {
		if err := send(ca, alice, []string{bob}, message(alice, bob, "x")); err != nil {
			t.Fatal(err)
		}
		e.clock.Add(2 * time.Hour)
	}
	if err := send(ca, alice, []string{bob}, message(alice, bob, "x")); code(err) != 451 {
		t.Fatalf("daily: %v", err)
	}
	ca.Reset()
	// The domain: alice's two sends are older than an hour; bob sends 3.
	for i := 0; i < 3; i++ {
		if err := send(cb, bob, []string{alice}, message(bob, alice, "x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := send(cb, bob, []string{alice}, message(bob, alice, "x")); code(err) != 451 {
		t.Fatalf("domain: %v", err)
	}
}

func TestLimitsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	cl := &clock{}
	cl.t.Store(time.Now().UnixNano())
	box := config.Mailbox{StorageID: idAlice, Address: alice, SendPerHour: 1}
	d := directory.New(testConfig(t))
	open := func() (*storage.Store, *Limiter) {
		st, err := storage.Open(dir, storage.Options{Now: cl.Now})
		if err != nil {
			t.Fatal(err)
		}
		return st, NewLimiter(st, cl.Now, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	st, l := open()
	if err := l.Reserve(context.Background(), d, box); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, l = open()
	defer st.Close()
	if err := l.Reserve(context.Background(), d, box); code(err) != 451 {
		t.Fatalf("after restart: %v", err)
	}
}

func TestBlockOnManyNetworks(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	box := config.Mailbox{StorageID: idAlice, Address: alice}
	// Many addresses of one IPv6 /64 and of one IPv4 /24 are one network each.
	for i := 1; i <= 30; i++ {
		e.limits.SignedIn(ctx, box, netip.MustParseAddr(fmt.Sprintf("2001:db8::%x", i)))
		e.limits.SignedIn(ctx, box, netip.MustParseAddr(fmt.Sprintf("192.0.2.%d", i)))
	}
	for i := 1; i <= MaxNetworks-2; i++ {
		e.limits.SignedIn(ctx, box, netip.MustParseAddr(fmt.Sprintf("198.51.%d.1", i)))
	}
	if b, _ := e.store.Blocked(ctx, idAlice); b.Blocked {
		t.Fatal("blocked at 10 networks")
	}
	// Networks older than an hour do not count.
	e.clock.Add(61 * time.Minute)
	e.limits.SignedIn(ctx, box, netip.MustParseAddr("203.0.113.1"))
	if b, _ := e.store.Blocked(ctx, idAlice); b.Blocked {
		t.Fatal("blocked by old networks")
	}
	for i := 1; i <= MaxNetworks; i++ {
		e.limits.SignedIn(ctx, box, netip.MustParseAddr(fmt.Sprintf("10.%d.0.1", i)))
	}
	b, _ := e.store.Blocked(ctx, idAlice)
	if !b.Blocked || !strings.Contains(b.Reason, "networks") {
		t.Fatalf("not blocked: %+v", b)
	}
	if _, err := e.store.Unblock(ctx, idAlice); err != nil {
		t.Fatal(err)
	}
	e.limits.SignedIn(ctx, box, netip.MustParseAddr("10.200.0.1"))
	if b, _ := e.store.Blocked(ctx, idAlice); b.Blocked {
		t.Fatal("blocked again after unblock")
	}
}

func TestCRLFWriter(t *testing.T) {
	for in, want := range map[string]string{
		"a\nb\n":       "a\r\nb\r\n",
		"a\r\nb\r\n":   "a\r\nb\r\n",
		"a\r\n\nb\n":   "a\r\n\r\nb\r\n",
		"no newline":   "no newline",
		"\n\n":         "\r\n\r\n",
		"mixed\r\nx\n": "mixed\r\nx\r\n",
	} {
		// Byte by byte, so CR and LF arrive in different writes.
		var b strings.Builder
		w := &crlfWriter{w: &b}
		for i := 0; i < len(in); i++ {
			w.Write([]byte{in[i]})
		}
		if b.String() != want {
			t.Errorf("%q: %q", in, b.String())
		}
	}
}

func TestForwardOnlyAlias(t *testing.T) {
	e := start(t)
	cfg := testConfig(t)
	cfg.Aliases = append(cfg.Aliases, config.Alias{Address: "out@example.org", Targets: []string{"away@remote.test"}})
	e.dir.Store(directory.New(cfg))
	c := e.client(alice)
	if err := send(c, alice, []string{"out@example.org"}, message(alice, "out@example.org", "hi")); err != nil {
		t.Fatal(err)
	}
	e.q.Flush()
	got := e.sink.Messages()
	// The sender is our own mailbox: no SRS.
	if len(got) != 1 || got[0].From != alice || len(got[0].To) != 1 || got[0].To[0] != "away@remote.test" {
		t.Fatalf("sink %+v", got)
	}
}
