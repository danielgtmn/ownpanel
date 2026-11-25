package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
)

func TestMailHostHandler(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	svc.opts.PanelUpstream = "127.0.0.1:8600"
	h := svc.MailHostHandler(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("account")) }),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("panel")) }),
	)
	get := func(host string) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/settings", nil)
		req.Host = host
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	for host, want := range map[string]string{
		testMailHost: "account", strings.ToUpper(testMailHost) + ":443": "account", testMailHost + ".": "account",
		"panel.example.org": "panel", "127.0.0.1:8600": "panel", "other." + testMailHost: "panel",
	} {
		if got := get(host); got != want {
			t.Errorf("%s: %s, want %s", host, got, want)
		}
	}
	// A mail host that is the panel domain is the panel.
	if _, err := svc.UpdateSettings(context.Background(), admin, SettingsUpdate{PanelDomain: strp(testMailHost)}); err != nil {
		t.Fatal(err)
	}
	if got := get(testMailHost); got != "panel" {
		t.Fatalf("mail host = panel domain: %s", got)
	}
}

func TestMailWebSite(t *testing.T) {
	w := newMailWorld(t)
	svc := w.svc
	ctx := context.Background()
	svc.now = time.Now
	svc.opts.PanelUpstream = "127.0.0.1:8600"
	svc.opts.TLSMode = caddy.TLSInternal
	selfService := func() MailHealthCheck {
		t.Helper()
		h, err := svc.MailHealth(ctx, admin)
		if err != nil {
			t.Fatal(err)
		}
		return healthCheck(t, h, HealthSelfService)
	}

	// Without the mail service's web socket: no site, and health says so.
	if mw := svc.caddyOptions().MailWeb; mw.Host != "" {
		t.Fatalf("site without a web socket: %+v", mw)
	}
	if c := selfService(); c.State != HealthWarning || !strings.Contains(c.Detail, "Updating") {
		t.Fatalf("no socket: %+v", c)
	}

	svc.opts.MailWebSocket = "/run/ownpanel-mail-web/web.sock"
	// No certificate yet.
	if mw := svc.caddyOptions().MailWeb; mw.Host != testMailHost || len(mw.Certificates) != 0 {
		t.Fatalf("before the certificate: %+v", mw)
	}
	if c := selfService(); c.State != HealthWarning || !strings.Contains(c.Detail, "certificate") {
		t.Fatalf("no certificate: %+v", c)
	}
	svc.certs = newMailCerts(t.TempDir(), caddy.TLSInternal)
	svc.refreshMailCerts(ctx, true)
	mw := svc.caddyOptions().MailWeb
	if mw.Host != testMailHost || mw.Socket != "/run/ownpanel-mail-web/web.sock" || len(mw.Certificates) != 1 ||
		mw.Certificates[0].CertPEM != svc.mailCertificates()[0].CertPEM || !strings.Contains(mw.Certificates[0].KeyPEM, "PRIVATE KEY") {
		t.Fatalf("site: %+v", mw.Host)
	}
	if c := selfService(); c.State != HealthOK || !strings.Contains(c.Detail, testMailHost) {
		t.Fatalf("ready: %+v", c)
	}
	// The web server gets the site on the next sync.
	if err := svc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	web := svc.web.(*fakeWeb)
	web.mu.Lock()
	last := web.cfgs[len(web.cfgs)-1]
	web.mu.Unlock()
	if !strings.Contains(last, "unix//run/ownpanel-mail-web/web.sock") || !strings.Contains(last, "load_pem") {
		t.Fatal("site not in the web server's config")
	}

	// The same name as the panel: no site, a warning.
	if _, err := svc.UpdateSettings(ctx, admin, SettingsUpdate{PanelDomain: strp(testMailHost)}); err != nil {
		t.Fatal(err)
	}
	if mw := svc.caddyOptions().MailWeb; mw.Host != "" {
		t.Fatalf("site on the panel domain: %+v", mw.Host)
	}
	if c := selfService(); c.State != HealthWarning || !strings.Contains(c.Detail, "different from the panel's address") {
		t.Fatalf("same host: %+v", c)
	}
}
