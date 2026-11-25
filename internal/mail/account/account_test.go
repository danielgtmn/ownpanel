package account

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/mail/config"
	"github.com/danielgtmn/ownpanel/internal/mail/mailtest"
	"github.com/danielgtmn/ownpanel/internal/mail/server"
	"github.com/danielgtmn/ownpanel/internal/mail/signin"
	"github.com/danielgtmn/ownpanel/internal/mail/web"
)

const (
	host     = "mail.example.net"
	address  = "alice@example.org"
	password = "alice-password-1"
)

var idAlice = mailtest.ID('a')

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t      *testing.T
	srv    *server.Server
	h      *Handler
	web    http.Handler
	cfg    *config.Config
	clk    *clock
	logs   *bytes.Buffer
	logsMu *sync.Mutex
	lastIP string
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	logs, mu := &bytes.Buffer{}, &sync.Mutex{}
	log := slog.New(slog.NewTextHandler(lockedWriter{mu, logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, err := server.New(server.Options{DataDir: t.TempDir(), Logger: log, FreeBytes: func() (int64, error) { return 10 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Store().Close() })
	cfg := &config.Config{
		Hostname:       host,
		Certificates:   []config.Certificate{mailtest.Cert(t, host, false)},
		Domains:        []config.Domain{{Name: "example.org"}},
		AppPasswordKey: config.NewAppPasswordKey(),
		Mailboxes: []config.Mailbox{
			{StorageID: idAlice, Address: address, PasswordHash: mailtest.CheapHash(password), QuotaBytes: 1 << 30, Revision: 3},
			{StorageID: mailtest.ID('b'), Address: "bob@example.org", PasswordHash: mailtest.CheapHash("bob-password-1")},
		},
		Limits: config.Limits{MaxMessageBytes: 10000},
	}
	if err := srv.Load(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Now()}
	h := New(Options{Backend: srv, Logger: log, Now: clk.now})
	return &env{t: t, srv: srv, h: h, web: web.Handler(web.Options{Account: h}), cfg: cfg, clk: clk, logs: logs, logsMu: mu, lastIP: "192.0.2.10"}
}

type req struct {
	method, path, body string
	cookie             string
	ip                 string
	header             map[string]string
	host               string
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.t.Helper()
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	hr.Host = host
	if r.host != "" {
		hr.Host = r.host
	}
	ip := r.ip
	if ip == "" {
		ip = e.lastIP
	}
	hr.Header.Set("X-Forwarded-For", ip)
	if r.method != "GET" {
		hr.Header.Set(CSRFHeader, "1")
		hr.Header.Set("Origin", "https://"+host)
		hr.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if r.body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.header {
		if v == "" {
			hr.Header.Del(k)
		} else {
			hr.Header.Set(k, v)
		}
	}
	if r.cookie != "" {
		hr.AddCookie(&http.Cookie{Name: CookieName, Value: r.cookie})
	}
	rec := httptest.NewRecorder()
	e.web.ServeHTTP(rec, hr)
	return rec
}

func want(t *testing.T, rec *httptest.ResponseRecorder, code int, contains string) {
	t.Helper()
	if rec.Code != code || !strings.Contains(rec.Body.String(), contains) {
		t.Fatalf("want %d %q, got %d %s", code, contains, rec.Code, rec.Body)
	}
}

func signInBody(addr, pw string) string {
	b, _ := json.Marshal(map[string]string{"address": addr, "password": pw})
	return string(b)
}

// signIn returns the session cookie's value.
func (e *env) signIn(addr, pw string) string {
	e.t.Helper()
	rec := e.do(req{method: "POST", path: "/account/api/session", body: signInBody(addr, pw)})
	want(e.t, rec, 200, `"address":"`+strings.ToLower(addr)+`"`)
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c.Value
		}
	}
	e.t.Fatal("no cookie")
	return ""
}

func (e *env) me(cookie string) (meView, int) {
	e.t.Helper()
	rec := e.do(req{method: "GET", path: "/account/api/me", cookie: cookie})
	var v meView
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			e.t.Fatal(err)
		}
	}
	return v, rec.Code
}

func (e *env) mailbox() *config.Mailbox {
	m, _ := e.srv.Directory().MailboxByStorageID(idAlice)
	return m
}

func (e *env) accountEvents() []config.Event {
	evs, err := e.srv.Store().Events(context.Background(), 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []config.Event
	for _, ev := range evs {
		if ev.Account != nil {
			out = append(out, ev)
		}
	}
	return out
}

func TestSignIn(t *testing.T) {
	e := newEnv(t)
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, "wrong-password")}), 401, "Wrong address or password")
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody("nobody@example.org", password)}), 401, "Wrong address or password")

	rec := e.do(req{method: "POST", path: "/account/api/session", body: signInBody("Alice@Example.org", password)})
	want(t, rec, 200, `"quota_bytes":1073741824`)
	c := rec.Result().Cookies()
	if len(c) != 1 || c[0].Name != CookieName || !c[0].Secure || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode ||
		c[0].Path != "/" || c[0].Domain != "" || len(c[0].Value) != 43 {
		t.Fatalf("cookie %+v", c)
	}
	v, code := e.me(c[0].Value)
	if code != 200 || v.Address != address || v.Client.Server != host || v.Client.IMAPPort != 993 || !v.AppPasswordsAvailable ||
		len(v.AppPasswords) != 0 {
		t.Fatalf("me %d %+v", code, v)
	}
	// Only a hash of the session id is stored.
	if _, ok, _ := e.srv.Store().AccountSessionByHash(context.Background(), []byte(c[0].Value)); ok {
		t.Fatal("session id stored as is")
	}

	// Not signed in, or a made-up cookie.
	want(t, e.do(req{method: "GET", path: "/account/api/me"}), 401, "Sign in again")
	want(t, e.do(req{method: "GET", path: "/account/api/me", cookie: strings.Repeat("A", 43)}), 401, "")

	// Sign-out ends the session.
	want(t, e.do(req{method: "DELETE", path: "/account/api/session", cookie: c[0].Value}), 204, "")
	if _, code := e.me(c[0].Value); code != 401 {
		t.Fatal("session survived sign-out")
	}
}

// TestAppPasswordsAndSwitch: an app password never signs in here, and
// "mail programs only with app passwords" does not keep the mailbox out.
func TestAppPasswordsAndSwitch(t *testing.T) {
	e := newEnv(t)
	cookie := e.signIn(address, password)
	rec := e.do(req{method: "PUT", path: "/account/api/app-passwords-only", cookie: cookie, body: `{"app_passwords_only":true}`})
	want(t, rec, 200, `"app_passwords_only":true`)
	if !e.mailbox().AppPasswordsOnly {
		t.Fatal("switch not applied")
	}
	// Still signs in here with the main password.
	e.signIn(address, password)

	rec = e.do(req{method: "POST", path: "/account/api/app-passwords", cookie: cookie, body: `{"name":"Phone"}`})
	want(t, rec, 201, `"password":"op_`)
	var created struct {
		AppPassword appPasswordView `json:"app_password"`
		Password    string          `json:"password"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	token := created.Password
	if _, _, ok := config.ParseAppPassword(token); !ok || created.AppPassword.Name != "Phone" {
		t.Fatalf("%+v", created)
	}
	// It works for mail programs ...
	if _, err := signin.New(signin.Options{}).Check(e.srv.Directory(), mustIP("192.0.2.1"), address, token); err != nil {
		t.Fatalf("app password for mail programs: %v", err)
	}
	// ... never here.
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, token)}), 401, "Wrong address or password")

	// Shown once: neither me, nor the events, nor the log carry it.
	meRec := e.do(req{method: "GET", path: "/account/api/me", cookie: cookie})
	want(t, meRec, 200, `"name":"Phone"`)
	mac := e.mailbox().AppPasswords[0].MAC
	_, secret, _ := config.ParseAppPassword(token)
	evs := e.accountEvents()
	evJSON, _ := json.Marshal(evs)
	e.logsMu.Lock()
	logs := e.logs.String()
	e.logsMu.Unlock()
	for name, s := range map[string]string{"me": meRec.Body.String(), "events": string(evJSON), "log": logs} {
		if strings.Contains(s, secret) {
			t.Fatalf("token in %s: %s", name, s)
		}
	}
	if strings.Contains(meRec.Body.String(), mac) {
		t.Fatal("MAC in me")
	}
	if last := evs[len(evs)-1]; last.Kind != config.EventAppPasswordCreated || last.Account.AppPassword.MAC != mac {
		t.Fatalf("event %+v", last)
	}

	// Revoke.
	want(t, e.do(req{method: "DELETE", path: "/account/api/app-passwords/" + created.AppPassword.ID, cookie: cookie}), 204, "")
	if len(e.mailbox().AppPasswords) != 0 {
		t.Fatal("not revoked")
	}
	want(t, e.do(req{method: "DELETE", path: "/account/api/app-passwords/" + created.AppPassword.ID, cookie: cookie}), 404, "")
	if _, err := signin.New(signin.Options{}).Check(e.srv.Directory(), mustIP("192.0.2.1"), address, token); err == nil {
		t.Fatal("revoked app password still works")
	}

	// Bad names.
	want(t, e.do(req{method: "POST", path: "/account/api/app-passwords", cookie: cookie, body: `{"name":"  "}`}), 400, "Enter the device")
	want(t, e.do(req{method: "POST", path: "/account/api/app-passwords", cookie: cookie, body: `{"name":"a<b"}`}), 400, "one line")
	want(t, e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"Alice Example"}`}), 200, `"display_name":"Alice Example"`)
	want(t, e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"x\ny"}`}), 400, "")
}

func TestThrottle(t *testing.T) {
	e := newEnv(t)
	// Per address and /48: failures from one /64 ...
	for range signin.PairLimit {
		e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, "wrong-password"), ip: "2001:db8:1:1::1"})
	}
	// ... hold back the whole /48, also with the right password.
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, password), ip: "2001:db8:1:ffff::2"}), 429, "Too many")
	// Other mailboxes from there and this mailbox from elsewhere go on.
	e.do(req{method: "POST", path: "/account/api/session", body: signInBody("bob@example.org", "bob-password-1"), ip: "2001:db8:1:1::1"})
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, password), ip: "2001:db8:2::1"}), 200, "")
	// IPv4 counts per address.
	for range signin.PairLimit {
		e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, "wrong-password"), ip: "198.51.100.7"})
	}
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, password), ip: "198.51.100.7"}), 429, "")
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, password), ip: "198.51.100.8"}), 200, "")

	// Mail programs have their own throttle (a Checker of their own): the
	// failures here did not count there.
	if _, err := signin.New(signin.Options{}).Check(e.srv.Directory(), mustIP("198.51.100.7"), address, password); err != nil {
		t.Fatal(err)
	}
}

func TestCSRFAndHost(t *testing.T) {
	e := newEnv(t)
	body := signInBody(address, password)
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: body, header: map[string]string{CSRFHeader: ""}}), 403, CSRFHeader)
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: body, header: map[string]string{"Origin": "https://evil.example"}}), 403, "other web sites")
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: body, header: map[string]string{"Origin": "http://" + host}}), 403, "other web sites")
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: body, header: map[string]string{"Sec-Fetch-Site": "same-site"}}), 403, "other web sites")
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: body, host: "panel.example.net"}), 404, "")
	// Without Origin and Sec-Fetch-Site (not a browser), the header is enough.
	cookie := e.signIn(address, password)
	want(t, e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"A"}`,
		header: map[string]string{"Origin": "", "Sec-Fetch-Site": ""}}), 200, "")
	want(t, e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"B"}`,
		header: map[string]string{CSRFHeader: ""}}), 403, "")
	// Size limit and unknown fields.
	want(t, e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"` + strings.Repeat("x", MaxBodyBytes) + `"}`}), 413, "")
	want(t, e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"B","x":1}`}), 400, "")
}

func TestPasswordChange(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cookie := e.signIn(address, password)
	other := e.signIn(address, password)
	// An app password to revoke along.
	want(t, e.do(req{method: "POST", path: "/account/api/app-passwords", cookie: cookie, body: `{"name":"Laptop"}`}), 201, "")

	pw := func(cur, next string, revoke bool) string {
		b, _ := json.Marshal(map[string]any{"current_password": cur, "new_password": next, "revoke_app_passwords": revoke})
		return string(b)
	}
	want(t, e.do(req{method: "PUT", path: "/account/api/password", cookie: cookie, body: pw("wrong-one-1", "new-password-1", false)}), 400, "current password isn't right")
	want(t, e.do(req{method: "PUT", path: "/account/api/password", cookie: cookie, body: pw(password, "short", false)}), 400, "between 10 and 200")
	want(t, e.do(req{method: "PUT", path: "/account/api/password", cookie: cookie, body: pw(password, "op_aaaaaaaaaaaaaaaa_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false)}), 400, "app password")
	want(t, e.do(req{method: "PUT", path: "/account/api/password", cookie: cookie, body: pw(password, "new-password-1", true)}), 200, `"app_passwords":[]`)

	// This session stays, the other one ended; the new password signs in.
	if _, code := e.me(cookie); code != 200 {
		t.Fatal("own session ended")
	}
	if _, code := e.me(other); code != 401 {
		t.Fatal("other session survived the password change")
	}
	want(t, e.do(req{method: "POST", path: "/account/api/session", body: signInBody(address, password)}), 401, "")
	e.signIn(address, "new-password-1")

	// Reported to the panel with the hash, never the password.
	evs := e.accountEvents()
	var changed *config.Event
	for i := range evs {
		if evs[i].Kind == config.EventPasswordChanged {
			changed = &evs[i]
		}
	}
	if changed == nil || changed.Account.PasswordHash != e.mailbox().PasswordHash || changed.Account.Revision != 5 {
		t.Fatalf("events %+v", evs)
	}
	if last := evs[len(evs)-1]; last.Kind != config.EventAppPasswordRevoked || last.Account.Revision != 6 {
		t.Fatalf("revoked along: %+v", last)
	}
	all, _ := json.Marshal(evs)
	if strings.Contains(string(all), "new-password-1") {
		t.Fatal("password in events")
	}

	// A push the panel built before it saw the change keeps the new
	// password and this session; so does the push that includes it.
	if err := e.srv.Load(ctx, e.cfg); err != nil {
		t.Fatal(err)
	}
	if _, code := e.me(cookie); code != 200 {
		t.Fatal("old push ended the session")
	}
	e.cfg.Mailboxes[0].PasswordHash = changed.Account.PasswordHash
	e.cfg.Mailboxes[0].Revision = 6
	if err := e.srv.Load(ctx, e.cfg); err != nil {
		t.Fatal(err)
	}
	if _, code := e.me(cookie); code != 200 {
		t.Fatal("the panel's push of the same password ended the session")
	}

	// A reset by the panel ends every session at once.
	e.cfg.Mailboxes[0].PasswordHash = mailtest.CheapHash("reset-by-admin")
	if err := e.srv.Load(ctx, e.cfg); err != nil {
		t.Fatal(err)
	}
	if _, code := e.me(cookie); code != 401 {
		t.Fatal("session survived a reset by the panel")
	}
}

func TestSessionEnds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cookie := e.signIn(address, password)

	// Idle: a request within the timeout keeps it going.
	e.clk.add(IdleTimeout - time.Minute)
	if _, code := e.me(cookie); code != 200 {
		t.Fatal("ended early")
	}
	e.clk.add(IdleTimeout - time.Minute)
	if _, code := e.me(cookie); code != 200 {
		t.Fatal("idle timeout not renewed")
	}
	e.clk.add(IdleTimeout)
	if _, code := e.me(cookie); code != 401 {
		t.Fatal("idle session still valid")
	}

	// Absolute: MaxAge even when used all the time.
	cookie = e.signIn(address, password)
	for range int(MaxAge / (20 * time.Minute)) {
		e.clk.add(20 * time.Minute)
		e.me(cookie)
	}
	if _, code := e.me(cookie); code != 401 {
		t.Fatal("session older than MaxAge")
	}

	// Deleting the mailbox ends its sessions; Prune cleans them up.
	cookie = e.signIn(address, password)
	bob := e.signIn("bob@example.org", "bob-password-1")
	e.cfg.Mailboxes = e.cfg.Mailboxes[1:]
	if err := e.srv.Load(ctx, e.cfg); err != nil {
		t.Fatal(err)
	}
	e.h.Prune(ctx)
	if _, code := e.me(cookie); code != 401 {
		t.Fatal("session of a deleted mailbox")
	}
	if _, code := e.me(bob); code != 200 {
		t.Fatal("prune ended another mailbox's session")
	}
}

func TestAppleSetupAndWriteLimit(t *testing.T) {
	e := newEnv(t)
	cookie := e.signIn(address, password)
	rec := e.do(req{method: "GET", path: "/account/api/apple-setup", cookie: cookie})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/x-apple-aspen-config" ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="alice@example.org.mobileconfig"` ||
		!strings.Contains(rec.Body.String(), "<key>IncomingMailServerHostName</key><string>"+host+"</string>") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	want(t, e.do(req{method: "GET", path: "/account/api/apple-setup"}), 401, "")

	for i := range WriteLimit {
		if rec := e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"n` + string(rune('a'+i%26)) + `"}`}); rec.Code != 200 {
			t.Fatalf("write %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	want(t, e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"x"}`}), 429, "Too many changes")
	if _, code := e.me(cookie); code != 200 {
		t.Fatal("reading is not limited")
	}
	e.clk.add(WriteWindow)
	want(t, e.do(req{method: "PUT", path: "/account/api/display-name", cookie: cookie, body: `{"display_name":"x"}`}), 200, "")
}

func mustIP(s string) netip.Addr { return netip.MustParseAddr(s) }
