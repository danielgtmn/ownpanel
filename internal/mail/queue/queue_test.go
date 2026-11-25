package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/directory"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/storage"
)

var idAlice = mailtest.ID('a')

type clock struct{ t atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.t.Store(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}
func (c *clock) Now() time.Time          { return time.Unix(0, c.t.Load()) }
func (c *clock) Add(d time.Duration)     { c.t.Add(int64(d)) }
func (c *clock) Since(t time.Time) int64 { return int64(c.Now().Sub(t)) }

type env struct {
	t      *testing.T
	dir    string
	store  *storage.Store
	q      *Queue
	clock  *clock
	sink   *mailtest.Sink
	res    *mailtest.Resolver
	events []config.Event
	mu     sync.Mutex
	dial   atomic.Int32 // dials
	down   atomic.Bool  // refuse connections
	// keepOpen makes connections ignore Close (the end of a running
	// attempt on shutdown) until the test ends.
	keepOpen atomic.Bool
}

// lingerConn ignores Close while its env's keepOpen is set.
type lingerConn struct {
	net.Conn
	e *env
}

func (c *lingerConn) Close() error {
	if c.e.keepOpen.Load() {
		return nil
	}
	return c.Conn.Close()
}

func testDir() *directory.Directory {
	return directory.New(&config.Config{
		Hostname:  "mail.example.net",
		Domains:   []config.Domain{{Name: "example.org"}},
		Mailboxes: []config.Mailbox{{StorageID: idAlice, Address: "alice@example.org", PasswordHash: mailtest.Hash}},
	})
}

func newEnv(t *testing.T, mode mailtest.SinkTLS) *env {
	e := &env{t: t, dir: t.TempDir(), clock: newClock(), sink: mailtest.NewSink(t, mode)}
	e.res = &mailtest.Resolver{
		MX: map[string][]*net.MX{
			"remote.test":  {{Host: "mx2.remote.test.", Pref: 20}, {Host: "mx1.remote.test.", Pref: 10}},
			"nomail.test":  {{Host: ".", Pref: 0}},
			"private.test": {{Host: "mx.private.test.", Pref: 10}, {Host: "mx.cgnat.test.", Pref: 20}, {Host: "localhost-mx.test.", Pref: 30}},
		},
		IP: map[string][]net.IPAddr{
			"mx1.remote.test":   mailtest.Localhost,
			"mx2.remote.test":   mailtest.Localhost,
			"implicit.test":     mailtest.Localhost,
			"v6only.test":       {{IP: net.ParseIP("::1")}},
			"mx.private.test":   {{IP: net.IPv4(10, 0, 0, 25)}, {IP: net.ParseIP("fd00::25")}},
			"mx.cgnat.test":     {{IP: net.IPv4(100, 64, 0, 1)}},
			"localhost-mx.test": mailtest.Localhost,
		},
	}
	e.open()
	return e
}

func (e *env) open() {
	st, err := storage.Open(e.dir, storage.Options{Now: e.clock.Now})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := st.Reconcile(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.store = st
	d := testDir()
	var dialer net.Dialer
	e.q = New(Options{
		Store:     st,
		Directory: func() *directory.Directory { return d },
		Resolver:  e.res,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			e.dial.Add(1)
			if e.down.Load() {
				return nil, errors.New("connection refused")
			}
			c, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			e.t.Cleanup(func() { c.Close() })
			return &lingerConn{Conn: c, e: e}, nil
		},
		// The sink listens on 127.0.0.1.
		AllowPrivate: true,
		Port:         e.sink.Port,
		Now:          e.clock.Now,
		Timeout:      5 * time.Second,
		Event: func(ev config.Event) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	e.t.Cleanup(func() { e.close() })
}

func (e *env) close() {
	if e.q != nil {
		e.q.Shutdown(context.Background())
		e.store.Close()
		e.q = nil
	}
}

const msg = "From: alice@example.org\r\nTo: bob@remote.test\r\nSubject: hello\r\n\r\nHi there.\r\n"

func (e *env) enqueue(sender string, rcpts ...string) {
	e.t.Helper()
	if err := e.q.Enqueue(context.Background(), strings.NewReader(msg), sender, idAlice, rcpts); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) length() int {
	n, err := e.q.Len(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) inbox() []string {
	e.t.Helper()
	_, msgs, err := e.store.Select(context.Background(), idAlice, storage.Inbox)
	if err != nil {
		if errors.Is(err, storage.ErrNoFolder) {
			return nil
		}
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

func TestDeliverWithAndWithoutTLS(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    mailtest.SinkTLS
		wantTLS bool
		dials   int32
	}{
		{"starttls", mailtest.SinkStartTLS, true, 1},
		{"plain", mailtest.SinkPlain, false, 1},
		// The handshake fails: the same host once more without TLS.
		{"broken tls", mailtest.SinkBrokenTLS, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.mode)
			e.enqueue("alice@example.org", "bob@remote.test", "carol@remote.test", "dave@implicit.test")
			if e.length() != 1 {
				t.Fatal("not queued")
			}
			e.q.Flush()
			got := e.sink.Messages()
			if len(got) != 2 {
				t.Fatalf("got %d messages", len(got))
			}
			for _, m := range got {
				if m.TLS != tc.wantTLS || m.From != "alice@example.org" || m.Helo != "mail.example.net" {
					t.Fatalf("message %+v", m)
				}
				if !strings.Contains(string(m.Data), "Hi there.") {
					t.Fatalf("data %q", m.Data)
				}
			}
			if len(got[0].To)+len(got[1].To) != 3 {
				t.Fatalf("recipients %v %v", got[0].To, got[1].To)
			}
			if e.length() != 0 {
				t.Fatal("still queued")
			}
			if d := e.dial.Load(); d != 2*tc.dials {
				t.Fatalf("dials %d", d)
			}
			if len(e.inbox()) != 0 {
				t.Fatal("bounce")
			}
		})
	}
}

func TestTemporaryFailureRetries(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	var refuse atomic.Bool
	refuse.Store(true)
	e.sink.SetRcpt(func(string) error {
		if refuse.Load() {
			return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 7, 1}, Message: "greylisted"}
		}
		return nil
	})
	e.enqueue("alice@example.org", "bob@remote.test")
	e.q.Flush()
	m := e.queued(1)
	if m.Rcpts[0].Attempts != 1 || m.Rcpts[0].State != storage.RcptPending ||
		!m.Rcpts[0].NextTry.Equal(e.clock.Now().Add(5*time.Minute).Truncate(time.Second)) ||
		!strings.Contains(m.Rcpts[0].LastError, "greylisted") {
		t.Fatalf("after first try %+v", m.Rcpts[0])
	}
	// Not due yet: nothing happens.
	e.clock.Add(4 * time.Minute)
	e.q.Flush()
	if e.queued(1).Rcpts[0].Attempts != 1 {
		t.Fatal("tried too early")
	}
	e.clock.Add(time.Minute)
	e.q.Flush()
	m = e.queued(1)
	if m.Rcpts[0].Attempts != 2 || !m.Rcpts[0].NextTry.Equal(e.clock.Now().Add(15*time.Minute).Truncate(time.Second)) {
		t.Fatalf("second try %+v", m.Rcpts[0])
	}
	refuse.Store(false)
	e.clock.Add(15 * time.Minute)
	e.q.Flush()
	if len(e.sink.Messages()) != 1 || e.length() != 0 || len(e.inbox()) != 0 {
		t.Fatal("not delivered after retry")
	}
}

func (e *env) queued(n int) storage.QueuedMessage {
	e.t.Helper()
	ms, err := e.store.QueueDue(context.Background(), e.clock.Now().Add(1000*time.Hour), 10)
	if err != nil || len(ms) != n {
		e.t.Fatalf("queued %v %d", err, len(ms))
	}
	m, err := e.store.QueueMessage(context.Background(), ms[0].ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func TestPermanentFailureBounces(t *testing.T) {
	e := newEnv(t, mailtest.SinkStartTLS)
	e.sink.SetRcpt(func(to string) error {
		if strings.HasPrefix(to, "nobody@") {
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
		}
		return nil
	})
	e.enqueue("alice@example.org", "bob@remote.test", "nobody@remote.test")
	e.q.Flush()
	if got := e.sink.Messages(); len(got) != 1 || len(got[0].To) != 1 || got[0].To[0] != "bob@remote.test" {
		t.Fatalf("sink %+v", got)
	}
	in := e.inbox()
	if len(in) != 1 {
		t.Fatalf("inbox %d", len(in))
	}
	b := in[0]
	for _, want := range []string{"Return-Path: <>", "From: Mail Delivery System <MAILER-DAEMON@mail.example.net>",
		"To: <alice@example.org>", "multipart/report; report-type=delivery-status", "Final-Recipient: rfc822; nobody@remote.test",
		"Status: 5.1.1", "550 5.1.1 no such user", "text/rfc822-headers", "Subject: hello"} {
		if !strings.Contains(b, want) {
			t.Errorf("bounce lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "Hi there.") {
		t.Error("bounce contains the body")
	}
	if e.length() != 0 {
		t.Fatal("still queued")
	}
	if k := e.eventKinds(); len(k) != 1 || k[0] != config.EventBounced {
		t.Fatalf("events %v", k)
	}
	e.mu.Lock()
	if strings.Contains(e.events[0].Detail, "Hi there") || !strings.Contains(e.events[0].Detail, "nobody@remote.test") {
		t.Errorf("event detail %q", e.events[0].Detail)
	}
	e.mu.Unlock()
}

func TestNullSenderNotBounced(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.sink.SetRcpt(func(string) error {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no"}
	})
	e.enqueue("", "bob@remote.test")
	e.q.Flush()
	if len(e.inbox()) != 0 || e.length() != 0 {
		t.Fatal("bounced a bounce or kept it")
	}
}

func TestNullMXAndUnknownDomain(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.enqueue("alice@example.org", "x@nomail.test", "y@does-not-exist.test")
	e.q.Flush()
	if e.dial.Load() != 0 {
		t.Fatal("dialed")
	}
	in := e.inbox()
	if len(in) != 2 {
		t.Fatalf("bounces %d", len(in))
	}
	all := in[0] + in[1]
	if !strings.Contains(all, "Status: 5.1.10") || !strings.Contains(all, "null MX") || !strings.Contains(all, "Status: 5.1.2") {
		t.Fatalf("bounces:\n%s", all)
	}
}

func TestIPv6OnlyHostIsTemporary(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.enqueue("alice@example.org", "x@v6only.test")
	e.q.Flush()
	if e.dial.Load() != 0 || len(e.inbox()) != 0 {
		t.Fatal("dialed or bounced")
	}
	if m := e.queued(1); !strings.Contains(m.Rcpts[0].LastError, "IPv6") {
		t.Fatalf("error %q", m.Rcpts[0].LastError)
	}
}

func TestGiveUpAfterFiveDays(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.down.Store(true)
	e.enqueue("alice@example.org", "bob@remote.test")
	start := e.clock.Now()
	tries := 0
	for e.length() > 0 && tries < 100 {
		e.q.Flush()
		tries++
		next, ok, err := e.store.QueueNext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			e.clock.t.Store(next.UnixNano())
		}
	}
	if e.length() != 0 {
		t.Fatal("never gave up")
	}
	took := e.clock.Now().Sub(start)
	if took < 5*24*time.Hour || took > 5*24*time.Hour+time.Second {
		t.Fatalf("gave up after %v", took)
	}
	// 5 m, 15 m, 30 m, 1 h, 2 h, then every 4 h until 5 days: 6 + 28 tries
	// plus the first one and the last at the deadline.
	if tries < 30 || tries > 40 {
		t.Fatalf("%d tries", tries)
	}
	in := e.inbox()
	if len(in) != 1 || !strings.Contains(in[0], "Status: 4.4.7") || !strings.Contains(in[0], "gave up after 5 days") {
		t.Fatalf("bounce %v", in)
	}
}

func TestQueueSurvivesRestart(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.down.Store(true)
	e.enqueue("alice@example.org", "bob@remote.test")
	e.q.Flush()
	e.close()

	e.down.Store(false)
	e.open()
	if e.length() != 1 {
		t.Fatal("queue lost")
	}
	e.clock.Add(5 * time.Minute)
	e.q.Start()
	if got := e.sink.Wait(1, 5*time.Second); len(got) != 1 {
		t.Fatal("not delivered after restart")
	}
	for i := 0; e.length() != 0; i++ {
		if i > 200 {
			t.Fatal("still queued")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A stale list of due recipients (or two attempts at once) never delivers
// a recipient twice: each attempt claims its rows first.
func TestClaimPreventsDoubleDelivery(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.enqueue("alice@example.org", "bob@remote.test")
	ms, err := e.store.QueueDue(context.Background(), e.clock.Now(), 10)
	if err != nil || len(ms) != 1 {
		t.Fatalf("due %v %d", err, len(ms))
	}
	m := ms[0]
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		e.q.sem <- struct{}{}
		e.q.wg.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.q.work(groupKey{m.ID, "remote.test"}, m, slices.Clone(m.Rcpts))
		}()
	}
	wg.Wait()
	// And once more with the now stale list.
	e.q.sem <- struct{}{}
	e.q.wg.Add(1)
	e.q.work(groupKey{m.ID, "remote.test"}, m, slices.Clone(m.Rcpts))
	if got := e.sink.Messages(); len(got) != 1 {
		t.Fatalf("delivered %d times", len(got))
	}
	if e.length() != 0 {
		t.Fatal("still queued")
	}
}

// The failed state is saved before the bounce goes out, so a crash in
// between cannot bounce the same message again after the restart.
func TestFailedStateSavedBeforeBounce(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.sink.SetRcpt(func(string) error {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no"}
	})
	e.enqueue("alice@example.org", "nobody@remote.test")
	ms, err := e.store.QueueDue(context.Background(), e.clock.Now(), 10)
	if err != nil || len(ms) != 1 {
		t.Fatal(err)
	}
	var state storage.RcptState = -1
	e.q.opts.Event = func(config.Event) {
		m, err := e.store.QueueMessage(context.Background(), ms[0].ID)
		if err != nil {
			t.Error(err)
			return
		}
		state = m.Rcpts[0].State
	}
	e.q.Flush()
	if state != storage.RcptFailed {
		t.Fatalf("state when bouncing: %v", state)
	}
	if len(e.inbox()) != 1 || e.length() != 0 {
		t.Fatal("bounce")
	}
}

// Shutdown cuts an attempt short after the receiver took the message for
// one recipient and refused another for good: those are saved (and the
// refusal bounced); only the unfinished recipient is tried again.
func TestShutdownKeepsFinishedResults(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.keepOpen.Store(true)
	e.sink.SetRcpt(func(to string) error {
		switch {
		case strings.HasPrefix(to, "nobody@"):
			return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}
		case strings.HasPrefix(to, "carol@"):
			return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "try later"}
		}
		return nil
	})
	// The grace time ends while the receiver takes the message.
	e.sink.SetData(func() error { e.q.cancelWork(); return nil })
	e.enqueue("alice@example.org", "bob@remote.test", "nobody@remote.test", "carol@remote.test")
	ms, err := e.store.QueueDue(context.Background(), e.clock.Now(), 10)
	if err != nil || len(ms) != 1 {
		t.Fatalf("due %v %d", err, len(ms))
	}
	// One attempt, as a worker runs it (Flush would try the released
	// recipient again and again with the cancelled context).
	e.q.sem <- struct{}{}
	e.q.wg.Add(1)
	e.q.work(groupKey{ms[0].ID, "remote.test"}, ms[0], ms[0].Rcpts)
	if got := e.sink.Messages(); len(got) != 1 {
		t.Fatalf("delivered %d", len(got))
	}
	m := e.queued(1)
	byAddr := map[string]storage.QueuedRcpt{}
	for _, r := range m.Rcpts {
		byAddr[r.Address] = r
	}
	if r := byAddr["bob@remote.test"]; r.State != storage.RcptDelivered {
		t.Fatalf("delivered recipient %+v", r)
	}
	if r := byAddr["nobody@remote.test"]; r.State != storage.RcptFailed {
		t.Fatalf("refused recipient %+v", r)
	}
	if r := byAddr["carol@remote.test"]; r.State != storage.RcptPending || r.Attempts != 0 || r.NextTry.After(e.clock.Now()) {
		t.Fatalf("unfinished recipient %+v", r)
	}
	if in := e.inbox(); len(in) != 1 || !strings.Contains(in[0], "nobody@remote.test") || strings.Contains(in[0], "carol@") {
		t.Fatalf("bounces %d", len(in))
	}

	// After the restart only carol is tried again.
	e.keepOpen.Store(false)
	e.close()
	e.open()
	e.sink.SetRcpt(nil)
	e.sink.SetData(nil)
	e.q.Flush()
	got := e.sink.Messages()
	if len(got) != 2 || len(got[1].To) != 1 || got[1].To[0] != "carol@remote.test" {
		t.Fatalf("after restart %+v", got)
	}
	if e.length() != 0 {
		t.Fatal("still queued")
	}
}

// Mail servers with only non-public addresses are never connected to; a
// domain that has nothing else fails for good.
func TestPrivateMXRefused(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.q.opts.AllowPrivate = false
	e.enqueue("alice@example.org", "x@private.test", "y@implicit.test")
	e.q.Flush()
	if e.dial.Load() != 0 || len(e.sink.Messages()) != 0 {
		t.Fatalf("dialed %d", e.dial.Load())
	}
	if e.length() != 0 {
		t.Fatal("still queued")
	}
	in := e.inbox()
	if len(in) != 2 {
		t.Fatalf("bounces %d", len(in))
	}
	all := in[0] + in[1]
	for _, want := range []string{"Status: 5.4.4", "x@private.test", "y@implicit.test", "only private or local addresses"} {
		if !strings.Contains(all, want) {
			t.Fatalf("bounces lack %q:\n%s", want, all)
		}
	}
}

// A host with a public address that cannot be reached keeps the failure
// temporary, even when the other hosts are private.
func TestPrivateAndUnreachableMXIsTemporary(t *testing.T) {
	e := newEnv(t, mailtest.SinkPlain)
	e.q.opts.AllowPrivate = false
	e.res.MX["mixed.test"] = []*net.MX{{Host: "mx.private.test.", Pref: 10}, {Host: "mx.public.test.", Pref: 20}}
	e.res.IP["mx.public.test"] = []net.IPAddr{{IP: net.IPv4(8, 8, 8, 8)}}
	e.down.Store(true)
	e.enqueue("alice@example.org", "x@mixed.test")
	e.q.Flush()
	if e.dial.Load() != 1 || len(e.inbox()) != 0 {
		t.Fatalf("dials %d", e.dial.Load())
	}
	if m := e.queued(1); m.Rcpts[0].State != storage.RcptPending {
		t.Fatalf("state %+v", m.Rcpts[0])
	}
}

func TestPublic(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true}, {"2a00:1450:4001::1", true}, {"::ffff:8.8.8.8", true},
		{"127.0.0.1", false}, {"::1", false}, {"10.1.2.3", false}, {"172.30.0.5", false},
		{"192.168.1.1", false}, {"fd12::1", false}, {"169.254.1.1", false}, {"fe80::1", false},
		{"0.0.0.0", false}, {"::", false}, {"100.64.0.1", false}, {"100.127.255.254", false},
		{"224.0.0.1", false}, {"ff02::1", false}, {"255.255.255.255", false}, {"240.0.0.1", false},
		{"0.1.2.3", false}, {"192.0.0.8", false}, {"198.18.0.1", false}, {"192.0.2.1", false},
		{"2001:db8::1", false}, {"::ffff:127.0.0.1", false}, {"::ffff:10.0.0.1", false},
		// NAT64: the embedded IPv4 address decides.
		{"64:ff9b::808:808", true}, {"64:ff9b::7f00:1", false}, {"64:ff9b::a00:1", false},
		{"64:ff9b::a9fe:a9fe", false}, {"64:ff9b::c0a8:101", false}, {"64:ff9b::6440:1", false},
		{"64:ff9b:1::808:808", true}, {"64:ff9b:1:ab::a00:1", false}, {"64:ff9b:1::7f00:1", false},
		// The local-use prefix only in its /96 form.
		{"64:ff9b:1:808:8:800::", false}, {"64:ff9b:1:0:8:808::", false},
	} {
		if got := public(net.ParseIP(tc.ip)); got != tc.want {
			t.Errorf("public(%s) = %v", tc.ip, got)
		}
	}
}
