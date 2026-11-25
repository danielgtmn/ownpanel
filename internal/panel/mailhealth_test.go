package panel

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
)

// healthMail is the mail service with a status and outbound check the test
// controls.
type healthMail struct {
	*fakeMail
	mu        sync.Mutex
	listeners []config.Listener
	expires   time.Time
	outbound  config.OutboundCheck
	outErr    error
	outCalls  int
	block     chan struct{} // if set, CheckOutbound waits for it
}

func (h *healthMail) Status(ctx context.Context) (config.Status, error) {
	st, err := h.fakeMail.Status(ctx)
	h.mu.Lock()
	defer h.mu.Unlock()
	st.Listeners, st.CertificateExpires = h.listeners, h.expires
	return st, err
}

func (h *healthMail) CheckOutbound(ctx context.Context) (config.OutboundCheck, error) {
	h.mu.Lock()
	h.outCalls++
	block := h.block
	h.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return config.OutboundCheck{}, ctx.Err()
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.outbound, h.outErr
}

func (h *healthMail) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.outCalls
}

// ptrResolver adds reverse lookups to fakeResolver.
type ptrResolver struct {
	*fakeResolver
	mu   sync.Mutex
	ptr  map[string][]string
	perr map[string]error
}

func (p *ptrResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.perr[addr]; err != nil {
		return nil, err
	}
	if n, ok := p.ptr[addr]; ok {
		return n, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: addr, IsNotFound: true}
}

func healthCheck(t *testing.T, h MailHealth, id string) MailHealthCheck {
	t.Helper()
	for _, c := range h.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in %+v", id, h.Checks)
	return MailHealthCheck{}
}

func TestMailHealth(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	hm := &healthMail{fakeMail: w.mail, expires: w.now.Add(5 * 24 * time.Hour), outbound: config.OutboundCheck{OK: true, Target: "gmail-smtp-in.l.google.com:25"},
		listeners: []config.Listener{
			{Name: "smtp", Address: ":25", Error: "listen tcp :25: bind: address already in use"},
			{Name: "imaps", Address: ":993", Listening: true},
		}}
	res := &ptrResolver{fakeResolver: &fakeResolver{answers: map[string][]netip.Addr{testMailHost: {serverV4, serverV6}}, errs: map[string]error{}},
		ptr: map[string][]string{serverV4.String(): {"Mail.Host.Test."}}, perr: map[string]error{}}
	svc.mail, svc.resolver, svc.serverIPs = hm, res, []netip.Addr{serverV4, serverV6}

	h, err := svc.MailHealth(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if h.MailHost != testMailHost || len(h.Checks) != 8 {
		t.Fatalf("%+v", h)
	}
	order := []string{HealthService, HealthMailHostDNS, HealthPTR, HealthOutboundPort, HealthCertificate, HealthListeners, HealthPostmaster, HealthBlocklists}
	for i, c := range h.Checks {
		if c.ID != order[i] || c.Found == nil || c.Expected == nil || c.Detail == "" {
			t.Fatalf("check %d: %+v", i, c)
		}
	}
	want := map[string]string{
		HealthService: HealthOK, HealthMailHostDNS: HealthOK, HealthPTR: HealthWarning, HealthOutboundPort: HealthOK,
		HealthCertificate: HealthWarning, HealthListeners: HealthError, HealthPostmaster: HealthOK,
	}
	for id, st := range want {
		if c := healthCheck(t, h, id); c.State != st {
			t.Errorf("%s: %+v, want %s", id, c, st)
		}
	}
	if c := healthCheck(t, h, HealthPTR); !strings.Contains(c.Detail, serverV6.String()) || len(c.Expected) != 2 {
		t.Fatalf("ptr %+v", c)
	}
	if c := healthCheck(t, h, HealthListeners); !strings.Contains(c.Detail, "smtp (:25)") || !strings.Contains(c.Detail, "address already in use") {
		t.Fatalf("listeners %+v", c)
	}
	if c := healthCheck(t, h, HealthOutboundPort); c.Found[0] != "gmail-smtp-in.l.google.com:25" {
		t.Fatalf("outbound %+v", c)
	}

	// The outbound check is reused for 10 minutes.
	svc.MailHealth(ctx, rootRead)
	if hm.calls() != 1 {
		t.Fatalf("%d outbound checks", hm.calls())
	}
	w.now = w.now.Add(11 * time.Minute)
	hm.mu.Lock()
	hm.outbound = config.OutboundCheck{OK: false, Target: "x:25", Error: "connection timed out"}
	hm.mu.Unlock()
	h, _ = svc.MailHealth(ctx, admin)
	if c := healthCheck(t, h, HealthOutboundPort); hm.calls() != 2 || c.State != HealthError || !strings.Contains(c.Detail, "timed out") {
		t.Fatalf("after 11 minutes: %d calls, %+v", hm.calls(), c)
	}

	// IPv4 without PTR is an error; failed lookups are unknown.
	res.mu.Lock()
	res.ptr = map[string][]string{}
	res.mu.Unlock()
	h, _ = svc.MailHealth(ctx, admin)
	if c := healthCheck(t, h, HealthPTR); c.State != HealthError || !strings.Contains(c.Detail, serverV4.String()) {
		t.Fatalf("ptr %+v", c)
	}
	res.mu.Lock()
	res.perr = map[string]error{serverV4.String(): errors.New("timeout"), serverV6.String(): errors.New("timeout")}
	res.errs[testMailHost] = errors.New("server failure")
	res.mu.Unlock()
	h, _ = svc.MailHealth(ctx, admin)
	if c := healthCheck(t, h, HealthPTR); c.State != HealthUnknown {
		t.Fatalf("ptr %+v", c)
	}
	if c := healthCheck(t, h, HealthMailHostDNS); c.State != HealthUnknown {
		t.Fatalf("dns %+v", c)
	}

	// Without a postmaster address, an active domain without a mailbox
	// has nowhere to send postmaster mail.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PostmasterAddress: strp("")}); err != nil {
		t.Fatal(err)
	}
	if c := healthCheck(t, mustHealth(t, svc), HealthPostmaster); c.State != HealthOK {
		t.Fatalf("postmaster with mailboxes everywhere %+v", c)
	}
	w.mx.point("bobother.example.com", testMailHost)
	svc.mx = w.mx
	if d, err := svc.EnableMailDomain(ctx, bob, "bobother.example.com"); err != nil || !d.Active {
		t.Fatalf("%+v %v", d, err)
	}
	if c := healthCheck(t, mustHealth(t, svc), HealthPostmaster); c.State != HealthWarning || len(c.Found) != 1 || c.Found[0] != "bobother.example.com" {
		t.Fatalf("postmaster %+v", c)
	}
	if _, err := svc.CreateMailAlias(ctx, bob, "bobother.example.com", "postmaster", []string{"bob@example.org"}); err != nil {
		t.Fatal(err)
	}
	if c := healthCheck(t, mustHealth(t, svc), HealthPostmaster); c.State != HealthOK {
		t.Fatalf("postmaster with alias %+v", c)
	}

	// An unreachable mail service: never a failure of the request.
	w.mail.set(func(f *fakeMail) { f.statusErr = errors.New("connection refused") })
	h = mustHealth(t, svc)
	if healthCheck(t, h, HealthService).State != HealthError || healthCheck(t, h, HealthCertificate).State != HealthUnknown ||
		healthCheck(t, h, HealthListeners).State != HealthUnknown {
		t.Fatalf("unreachable %+v", h.Checks)
	}

	// Admins only.
	for _, a := range []Actor{bob, bobRead, bobLimited, rootLimited} {
		_, err := svc.MailHealth(ctx, a)
		wantKind(t, err, Forbidden)
	}
	_, err = svc.MailHealth(ctx, nobody)
	wantKind(t, err, Unauthenticated)
}

func mustHealth(t *testing.T, svc *Service) MailHealth {
	t.Helper()
	h, err := svc.MailHealth(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestMailHealthWithoutService(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	svc.mail, svc.serverIPs = nil, nil
	svc.resolver = &fakeResolver{answers: map[string][]netip.Addr{}, errs: map[string]error{}}
	h := mustHealth(t, svc)
	if healthCheck(t, h, HealthService).State != HealthError || healthCheck(t, h, HealthOutboundPort).State != HealthUnknown ||
		healthCheck(t, h, HealthMailHostDNS).State != HealthUnknown || healthCheck(t, h, HealthPTR).State != HealthUnknown {
		t.Fatalf("%+v", h.Checks)
	}
	// Without a mail host, its name cannot be checked.
	svc.settingsMu.Lock()
	svc.settings.mailHost = ""
	svc.settingsMu.Unlock()
	h = mustHealth(t, svc)
	if h.MailHost != "" || healthCheck(t, h, HealthMailHostDNS).State != HealthError {
		t.Fatalf("%+v", h)
	}
}

func TestMailHealthSlowOutboundCheck(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	hm := &healthMail{fakeMail: w.mail, expires: w.now.Add(60 * 24 * time.Hour), outbound: config.OutboundCheck{OK: true, Target: "x:25"},
		block: make(chan struct{})}
	svc.mail = hm
	svc.resolver = &fakeResolver{answers: map[string][]netip.Addr{}, errs: map[string]error{}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	h, err := svc.MailHealth(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if c := healthCheck(t, h, HealthOutboundPort); c.State != HealthUnknown || !strings.Contains(c.Detail, "being checked") {
		t.Fatalf("%+v", c)
	}
	// While it runs, no second check starts.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	svc.MailHealth(ctx2, admin)
	if hm.calls() != 1 {
		t.Fatalf("%d checks", hm.calls())
	}
	// When it finishes, the result is cached for the next request.
	close(hm.block)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, ok := svc.mailHealth.cached(svc.now()); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("result not cached")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if c := healthCheck(t, mustHealth(t, svc), HealthOutboundPort); c.State != HealthOK || hm.calls() != 1 {
		t.Fatalf("%+v, %d checks", c, hm.calls())
	}
}
