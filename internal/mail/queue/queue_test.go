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
			"remote.test": {{Host: "mx2.remote.test.", Pref: 20}, {Host: "mx1.remote.test.", Pref: 10}},
			"nomail.test": {{Host: ".", Pref: 0}},
		},
		IP: map[string][]net.IPAddr{
			"mx1.remote.test": mailtest.Localhost,
			"mx2.remote.test": mailtest.Localhost,
			"implicit.test":   mailtest.Localhost,
			"v6only.test":     {{IP: net.ParseIP("::1")}},
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
			return dialer.DialContext(ctx, network, addr)
		},
		Port:    e.sink.Port,
		Now:     e.clock.Now,
		Timeout: 5 * time.Second,
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
