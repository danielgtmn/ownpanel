package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// failingWeb fails every load while fail is set.
type failingWeb struct {
	mu   sync.Mutex
	fail bool
}

func (f *failingWeb) Load(context.Context, any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("caddy is down")
	}
	return nil
}

func (f *failingWeb) set(fail bool) {
	f.mu.Lock()
	f.fail = fail
	f.mu.Unlock()
}

// newSettingsEnv is a fresh install as main builds it: ACME, the panel
// upstream set, a fake interface list. flagDomain set means -panel-domain.
func newSettingsEnv(t *testing.T, flagDomain string) (*env, *failingWeb) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	web := &failingWeb{}
	svc, err := panel.New(panel.Config{
		Store:   st,
		Deploys: &deploy.Manager{Base: filepath.Join(dir, "sites")},
		Web:     web,
		Caddy: caddy.Options{
			AdminListen: "127.0.0.1:2019", TLSMode: caddy.TLSACME, HTTPPort: 80, HTTPSPort: 443,
			PanelUpstream: "127.0.0.1:8600", PanelDomain: flagDomain,
		},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
		Interfaces: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.5")}, nil
		},
		Resolver:      staticResolver{"panel.example.com": {netip.MustParseAddr("8.8.8.8")}},
		CertCheckAddr: "127.0.0.1:1", // nothing listens: pending
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureSetupCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, h: (&Server{Panel: svc}).Handler(), svc: svc, code: filepath.Join(dir, "setup-code")}, web
}

var settingsKeys = []string{"panel_domain", "panel_domain_locked", "server_ips", "server_ips_locked", "detected_ips",
	"acme_email", "acme_email_locked", "tls_mode", "setup_complete", "ip_access",
	"mail_host", "mail_host_locked", "postmaster_address",
	"mail_blocklists", "mail_blocklists_default", "mail_junk_score", "mail_greylisting"}

type settingsBody struct {
	PanelDomain       string   `json:"panel_domain"`
	PanelDomainLocked bool     `json:"panel_domain_locked"`
	ServerIPs         []string `json:"server_ips"`
	DetectedIPs       []struct {
		IP     string `json:"ip"`
		Public bool   `json:"public"`
	} `json:"detected_ips"`
	ACMEEmail     string `json:"acme_email"`
	TLSMode       string `json:"tls_mode"`
	SetupComplete bool   `json:"setup_complete"`
	IPAccess      bool   `json:"ip_access"`
}

func TestSettingsRoutesJSONShapes(t *testing.T) {
	e, _ := newSettingsEnv(t, "")
	session := func() json.RawMessage {
		rec := e.do(req{method: "GET", path: "/api/session", cookie: e.cookie})
		wantStatus(t, rec, 200, "")
		return json.RawMessage(rec.Body.Bytes())
	}
	before := session()
	wantKeys(t, before, "setup_required", "setup_complete", "user", "via", "password_reset")
	if !strings.Contains(string(before), `"setup_required":true,"setup_complete":false`) {
		t.Fatalf("fresh install: %s", before)
	}
	e.cookie = e.setup("root", "root-password-1")
	if s := session(); !strings.Contains(string(s), `"setup_required":false,"setup_complete":false`) {
		t.Fatalf("after creating the admin the wizard follows: %s", s)
	}

	rec := e.as("GET", "/api/settings", "")
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), settingsKeys...)
	if got := rec.Body.String(); !strings.Contains(got, `"server_ips":[]`) ||
		!strings.Contains(got, `"detected_ips":[{"ip":"8.8.8.8","public":true},{"ip":"10.0.0.5","public":false}]`) &&
			!strings.Contains(got, `"detected_ips":[{"ip":"10.0.0.5","public":false},{"ip":"8.8.8.8","public":true}]`) {
		t.Fatalf("settings: %s", got)
	}

	rec = e.as("PUT", "/api/settings", `{"panel_domain":"panel.example.com","server_ips":["8.8.8.8"],"acme_email":"ops@example.com"}`)
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), settingsKeys...)
	s := decode[settingsBody](t, rec)
	if s.PanelDomain != "panel.example.com" || len(s.ServerIPs) != 1 || s.ACMEEmail != "ops@example.com" || !s.IPAccess || s.TLSMode != "acme" {
		t.Fatalf("after PUT: %+v", s)
	}
	// Omitted fields stay, null too.
	rec = e.as("PUT", "/api/settings", `{"server_ips":null}`)
	if s := decode[settingsBody](t, rec); rec.Code != 200 || s.PanelDomain != "panel.example.com" || len(s.ServerIPs) != 1 {
		t.Fatalf("partial PUT: %d %s", rec.Code, rec.Body)
	}

	rec = e.as("GET", "/api/settings/checks", "")
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), "dns", "certificate")
	var checks map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &checks)
	wantKeys(t, checks["dns"], "state", "found", "expected")
	wantKeys(t, checks["certificate"], "state", "detail")
	if !strings.Contains(rec.Body.String(), `"dns":{"state":"points_here","found":["8.8.8.8"],"expected":["8.8.8.8"]}`) ||
		!strings.Contains(rec.Body.String(), `"certificate":{"state":"pending"`) {
		t.Fatalf("checks: %s", rec.Body)
	}

	rec = e.as("POST", "/api/settings/complete", "")
	wantStatus(t, rec, 200, "")
	wantKeys(t, rec.Body.Bytes(), settingsKeys...)
	if s := decode[settingsBody](t, rec); !s.SetupComplete || s.IPAccess {
		t.Fatalf("complete: %+v", s)
	}
	if rec := e.as("POST", "/api/settings/complete", ""); rec.Code != 200 {
		t.Fatalf("complete is idempotent: %d", rec.Code)
	}
	if s := session(); !strings.Contains(string(s), `"setup_complete":true`) {
		t.Fatalf("session after completion: %s", s)
	}
}

func TestSettingsRoutesStatusMapping(t *testing.T) {
	e, web := newSettingsEnv(t, "")
	e.cookie = e.setup("root", "root-password-1")
	if rec := e.as("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`); rec.Code != 200 {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}
	wantStatus(t, e.as("PUT", "/api/settings", `{"panel_domain":"203.0.113.5"}`), 400, "not an IP address")
	wantStatus(t, e.as("PUT", "/api/settings", `{"server_ips":["10.0.0.5"]}`), 400, "not a public address")
	wantStatus(t, e.as("PUT", "/api/settings", `{"server_ips":"8.8.8.8"}`), 400, "JSON like")
	wantStatus(t, e.as("PUT", "/api/settings", `{"acme_email":"nope"}`), 400, "e-mail")
	wantStatus(t, e.as("PUT", "/api/settings", `{"panel_domain":"blog.example.com"}`), 409, "already used by the site")
	web.set(true)
	wantStatus(t, e.as("PUT", "/api/settings", `{"panel_domain":"panel.example.com"}`), 502, "Saved, but the web server could not be updated")
	web.set(false)
	if s := decode[settingsBody](t, e.as("GET", "/api/settings", "")); s.PanelDomain != "panel.example.com" {
		t.Fatalf("saved despite the web server: %+v", s)
	}
	// The panel domain is no longer available for sites.
	wantStatus(t, e.as("PUT", "/api/sites/other", `{"domains":["panel.example.com"]}`), 400, "address of the panel")

	locked, _ := newSettingsEnv(t, "flag.example.com")
	locked.cookie = locked.setup("root", "root-password-1")
	wantStatus(t, locked.as("PUT", "/api/settings", `{"panel_domain":"other.example.com"}`), 409,
		"This value is set in the server configuration (-panel-domain) and cannot be changed here.")
	if s := decode[settingsBody](t, locked.as("GET", "/api/settings", "")); s.PanelDomain != "flag.example.com" || !s.PanelDomainLocked {
		t.Fatalf("locked: %+v", s)
	}
}

func TestSettingsRoutesPermissions(t *testing.T) {
	e, _ := newSettingsEnv(t, "")
	e.cookie = e.setup("root", "root-password-1")
	if rec := e.as("POST", "/api/users", `{"name":"bob","password":"bob-password-1","role":"member"}`); rec.Code != 201 {
		t.Fatalf("create bob: %d %s", rec.Code, rec.Body)
	}
	rec := e.do(req{method: "POST", path: "/api/login", body: `{"name":"bob","password":"bob-password-1"}`})
	bob := sessionCookie(t, rec)
	_, read := e.createToken("read", "null")
	_, write := e.createToken("write", "null")

	put := `{"acme_email":"ops@example.com"}`
	for _, tc := range []struct {
		name string
		r    req
		want int
	}{
		{"anonymous get", req{method: "GET", path: "/api/settings"}, 401},
		{"anonymous checks", req{method: "GET", path: "/api/settings/checks"}, 401},
		{"anonymous complete", req{method: "POST", path: "/api/settings/complete"}, 401},
		{"member get", req{method: "GET", path: "/api/settings", cookie: bob}, 403},
		{"member put", req{method: "PUT", path: "/api/settings", body: put, cookie: bob, csrf: true}, 403},
		{"member complete", req{method: "POST", path: "/api/settings/complete", cookie: bob, csrf: true}, 403},
		{"admin read token get", req{method: "GET", path: "/api/settings", token: read}, 200},
		{"admin read token checks", req{method: "GET", path: "/api/settings/checks", token: read}, 200},
		{"admin read token put", req{method: "PUT", path: "/api/settings", body: put, token: read}, 403},
		{"admin read token complete", req{method: "POST", path: "/api/settings/complete", token: read}, 403},
		{"admin write token put", req{method: "PUT", path: "/api/settings", body: put, token: write}, 200},
		{"admin session without CSRF header", req{method: "PUT", path: "/api/settings", body: put, cookie: e.cookie}, 403},
		{"admin session", req{method: "POST", path: "/api/settings/complete", cookie: e.cookie, csrf: true}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := e.do(tc.r); rec.Code != tc.want {
				t.Fatalf("%d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

// Before setup the panel is opened at https://<address>/ through Caddy:
// the peer is loopback, Caddy says https, the Host is the bare address
// (IPv6 in brackets). The cookie must be Secure and the browser's Origin
// must be accepted; another origin must not.
func TestIPAccessThroughProxy(t *testing.T) {
	for _, host := range []string{"203.0.113.5", "[2001:db8::5]", "203.0.113.5:8443", "[2001:db8::5]:8443"} {
		t.Run(host, func(t *testing.T) {
			e, _ := newSettingsEnv(t, "")
			proxied := func(r req) req {
				r.remote, r.host = "127.0.0.1:40000", host
				r.headers = map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "198.51.100.9", "Origin": "https://" + host}
				return r
			}
			body, _ := json.Marshal(map[string]string{"setup_code": e.setupCode(), "name": "root", "password": "root-password-1"})
			rec := e.do(proxied(req{method: "POST", path: "/api/setup", body: string(body)}))
			if rec.Code != 201 {
				t.Fatalf("setup by IP: %d %s", rec.Code, rec.Body)
			}
			c := rec.Result().Cookies()[0]
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("cookie %+v", c)
			}
			cookie := sessionCookie(t, rec)
			rec = e.do(proxied(req{method: "PUT", path: "/api/settings", body: `{"panel_domain":"panel.example.com"}`, cookie: cookie, csrf: true}))
			if rec.Code != 200 {
				t.Fatalf("PUT by IP: %d %s", rec.Code, rec.Body)
			}
			r := proxied(req{method: "PUT", path: "/api/settings", body: `{"panel_domain":"evil.example.com"}`, cookie: cookie, csrf: true})
			r.headers["Origin"] = "https://evil.example"
			if rec := e.do(r); rec.Code != 403 {
				t.Fatalf("foreign origin: %d", rec.Code)
			}
			r = proxied(req{method: "PUT", path: "/api/settings", body: `{"panel_domain":"evil.example.com"}`, cookie: cookie})
			if rec := e.do(r); rec.Code != 403 {
				t.Fatalf("missing CSRF header: %d", rec.Code)
			}
		})
	}
}

func TestSettingsRoutesSpamChecks(t *testing.T) {
	e, _ := newSettingsEnv(t, "")
	e.cookie = e.setup("root", "root-password-1")
	type spam struct {
		Blocklists []string `json:"mail_blocklists"`
		Default    bool     `json:"mail_blocklists_default"`
		JunkScore  float64  `json:"mail_junk_score"`
		Grey       bool     `json:"mail_greylisting"`
	}
	rec := e.as("GET", "/api/settings", "")
	if s := decode[spam](t, rec); !s.Default || len(s.Blocklists) != 1 || s.Blocklists[0] != "zen.spamhaus.org" || s.JunkScore != 5 || s.Grey {
		t.Fatalf("defaults: %s", rec.Body)
	}
	rec = e.as("PUT", "/api/settings", `{"mail_blocklists":["Bl.Example.org.","zen.spamhaus.org"],"mail_junk_score":7.5,"mail_greylisting":true}`)
	if s := decode[spam](t, rec); rec.Code != 200 || s.Default || strings.Join(s.Blocklists, ",") != "bl.example.org,zen.spamhaus.org" || s.JunkScore != 7.5 || !s.Grey {
		t.Fatalf("own list: %d %s", rec.Code, rec.Body)
	}
	// null keeps, [] turns off.
	rec = e.as("PUT", "/api/settings", `{"mail_blocklists":null}`)
	if s := decode[spam](t, rec); len(s.Blocklists) != 2 {
		t.Fatalf("null: %s", rec.Body)
	}
	rec = e.as("PUT", "/api/settings", `{"mail_blocklists":[]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"mail_blocklists":[],"mail_blocklists_default":false`) {
		t.Fatalf("off: %s", rec.Body)
	}
	rec = e.as("PUT", "/api/settings", `{"mail_blocklists_default":true}`)
	if s := decode[spam](t, rec); !s.Default || len(s.Blocklists) != 1 {
		t.Fatalf("default: %s", rec.Body)
	}
	for _, body := range []string{
		`{"mail_blocklists":["not a zone"]}`,
		`{"mail_blocklists":["a.example","a.example"]}`,
		`{"mail_blocklists":["1.example","2.example","3.example","4.example","5.example","6.example","7.example","8.example","9.example","10.example","11.example"]}`,
		`{"mail_blocklists_default":true,"mail_blocklists":["a.example"]}`,
		`{"mail_junk_score":0.5}`,
		`{"mail_junk_score":51}`,
	} {
		if rec := e.as("PUT", "/api/settings", body); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := e.as("PUT", "/api/settings", `{"mail_blocklists":"zen.spamhaus.org"}`); rec.Code != 400 {
		t.Fatalf("not a list: %d", rec.Code)
	}
}
