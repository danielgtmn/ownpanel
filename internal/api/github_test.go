package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/panzerschrank"
)

// newGitHubEnv is a panel with a domain and HTTPS, connected to a fake
// GitHub, with the admin "root" signed in.
func newGitHubEnv(t *testing.T) (*env, *githubtest.Server) {
	t.Helper()
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	gh.AddRepo("acme", "site", true)
	client, err := github.New(gh.URL)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, panzerschrank.KeySize)
	rand.Read(key)
	box, _ := panzerschrank.New(key)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "ownpanel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := panel.New(panel.Config{
		Store:   st,
		Deploys: &deploy.Manager{Base: filepath.Join(dir, "sites")},
		Web:     nopWeb{},
		Caddy: caddy.Options{AdminListen: "127.0.0.1:2019", TLSMode: caddy.TLSInternal, HTTPPort: 80, HTTPSPort: 443,
			PanelDomain: "panel.example.com", PanelUpstream: "127.0.0.1:8600", StorageDir: filepath.Join(dir, "caddy")},
		SetupCodeFile: filepath.Join(dir, "setup-code"),
		Secrets:       box,
		GitHub:        client,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureSetupCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, h: (&Server{Panel: svc}).Handler(), svc: svc, code: filepath.Join(dir, "setup-code")}
	e.cookie = e.setup("root", "root-password-1")
	return e, gh
}

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// fragmentQuery returns the query in the fragment of a redirect to the web
// app ("/#/settings?…").
func fragmentQuery(t *testing.T, location string) url.Values {
	t.Helper()
	rest, ok := strings.CutPrefix(location, "/#/settings?")
	if !ok {
		t.Fatalf("redirect to %q", location)
	}
	q, err := url.ParseQuery(rest)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

var (
	formAction   = regexp.MustCompile(`<form id="f" method="post" action="([^"]+)">`)
	formManifest = regexp.MustCompile(`name="manifest" value="([^"]*)"`)
)

func TestGitHubConnectOverHTTP(t *testing.T) {
	e, gh := newGitHubEnv(t)
	var bodies []string

	rec := e.as("GET", "/api/github", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ready":true`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	rec = e.as("POST", "/api/github/apps", `{"organization":"acme"}`)
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	start := decode[panel.GitHubStart](t, rec)

	// The manifest page: a same-site navigation, so the cookie comes along
	// (without the CSRF header, as for any page).
	rec = e.do(req{method: "GET", path: start.URL, cookie: e.cookie})
	if rec.Code != 200 {
		t.Fatalf("manifest page: %d %s", rec.Code, rec.Body)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action "+gh.URL+";") || !strings.Contains(csp, "script-src 'sha256-") ||
		strings.Contains(csp, "unsafe") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("policy: %s", csp)
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("headers: %v", rec.Header())
	}
	page := rec.Body.String()
	am, mm := formAction.FindStringSubmatch(page), formManifest.FindStringSubmatch(page)
	if am == nil || mm == nil {
		t.Fatalf("page: %s", page)
	}
	action, manifest := html.UnescapeString(am[1]), html.UnescapeString(mm[1])
	if !strings.HasPrefix(action, gh.URL+"/organizations/acme/settings/apps/new?state=") {
		t.Fatalf("action %s", action)
	}

	// GitHub creates the App and sends the browser back, without the
	// session cookie (another site).
	resp, err := noFollow.PostForm(action, url.Values{"manifest": {manifest}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	rec = e.do(req{method: "GET", path: back.RequestURI()})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	q := fragmentQuery(t, rec.Header().Get("Location"))
	if q.Get("github") != "created" || q.Get("code") == "" || q.Get("state") == "" {
		t.Fatalf("callback to %v", q)
	}
	// Nothing was stored by the callback itself.
	if conns, _ := e.svc.GitHubConnections(context.Background(), rootActor(t, e)); len(conns.Apps) != 0 {
		t.Fatal("the callback changed something")
	}

	code := q.Get("code")
	finish, _ := json.Marshal(map[string]string{"code": code, "state": q.Get("state")})
	// Without the CSRF header, from another site, or with a token: refused.
	if rec := e.do(req{method: "POST", path: "/api/github/apps/finish", body: string(finish), cookie: e.cookie}); rec.Code != 403 {
		t.Fatalf("without CSRF header: %d", rec.Code)
	}
	if rec := e.do(req{method: "POST", path: "/api/github/apps/finish", body: string(finish), cookie: e.cookie, csrf: true,
		headers: map[string]string{"Origin": "https://github.com"}}); rec.Code != 403 {
		t.Fatalf("from another site: %d", rec.Code)
	}
	_, token := e.createToken("write", "null")
	if rec := e.do(req{method: "POST", path: "/api/github/apps/finish", body: string(finish), token: token}); rec.Code != 403 {
		t.Fatalf("with a token: %d %s", rec.Code, rec.Body)
	}
	rec = e.as("POST", "/api/github/apps/finish", string(finish))
	if rec.Code != 201 {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	bodies = append(bodies, rec.Body.String())
	app := decode[panel.GitHubApp](t, rec)

	rec = e.as("POST", "/api/github/apps/"+strconv.FormatInt(app.ID, 10)+"/install", "")
	if rec.Code != 200 {
		t.Fatalf("install: %d %s", rec.Code, rec.Body)
	}
	inst := decode[panel.GitHubStart](t, rec)
	resp, err = noFollow.Get(inst.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ = url.Parse(resp.Header.Get("Location"))
	rec = e.do(req{method: "GET", path: back.RequestURI()})
	q = fragmentQuery(t, rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || q.Get("github") != "installed" || q.Get("installation_id") == "" || q.Get("state") == "" {
		t.Fatalf("setup callback: %d %v", rec.Code, q)
	}
	id, _ := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	body, _ := json.Marshal(map[string]any{"installation_id": id, "state": q.Get("state")})
	rec = e.as("POST", "/api/github/installations", string(body))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"account":"acme"`) {
		t.Fatalf("installed: %d %s", rec.Code, rec.Body)
	}
	bodies = append(bodies, rec.Body.String())

	rec = e.as("GET", "/api/github/installations/"+q.Get("installation_id")+"/repositories", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"full_name":"acme/site"`) {
		t.Fatalf("repositories: %d %s", rec.Code, rec.Body)
	}
	bodies = append(bodies, rec.Body.String())
	rec = e.as("POST", "/api/github/refresh", "")
	if rec.Code != 200 {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}
	bodies = append(bodies, rec.Body.String())
	rec = e.as("GET", "/api/github", "")
	bodies = append(bodies, rec.Body.String())
	rec = e.as("DELETE", "/api/github/apps/"+strconv.FormatInt(app.ID, 10), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"settings_url":"`+gh.URL+`/organizations/acme/settings/apps/`) {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	bodies = append(bodies, rec.Body.String())

	fake, _ := gh.App(app.AppID)
	all := strings.Join(bodies, "\n")
	for _, secret := range []string{fake.WebhookSecret, fake.ClientSecret, "PRIVATE KEY", code, "ghs_"} {
		if strings.Contains(all, secret) {
			t.Fatalf("an answer contains %q", secret)
		}
	}
}

func rootActor(t *testing.T, e *env) panel.Actor {
	t.Helper()
	a, err := e.svc.SessionActor(context.Background(), e.cookie)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestGitHubCallbacksOnlyRedirect(t *testing.T) {
	e, _ := newGitHubEnv(t)
	state := strings.Repeat("A", 43)
	for _, c := range []struct {
		path string
		want url.Values
	}{
		{"/api/github/app-created?code=abc123&state=" + state, url.Values{"github": {"created"}, "code": {"abc123"}, "state": {state}}},
		{"/api/github/app-created?code=abc123", url.Values{"github": {"failed"}}},
		{"/api/github/app-created?code=../../x&state=" + state, url.Values{"github": {"failed"}}},
		{"/api/github/app-created?code=abc&state=%3Cscript%3E", url.Values{"github": {"failed"}}},
		{"/api/github/app-created?code=abc&state=" + state + "&redirect_to=https://evil.example/", url.Values{"github": {"created"}, "code": {"abc"}, "state": {state}}},
		{"/api/github/installed?installation_id=42&setup_action=install&state=" + state, url.Values{"github": {"installed"}, "installation_id": {"42"}, "state": {state}}},
		{"/api/github/installed?installation_id=42&setup_action=update", url.Values{"github": {"installed"}, "installation_id": {"42"}}},
		{"/api/github/installed?installation_id=-1&state=x%22", url.Values{"github": {"installed"}}},
		{"/api/github/installed?setup_action=request", url.Values{"github": {"requested"}}},
	} {
		rec := e.do(req{method: "GET", path: c.path})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d", c.path, rec.Code)
		}
		got := fragmentQuery(t, rec.Header().Get("Location"))
		if got.Encode() != c.want.Encode() {
			t.Errorf("%s: got %v, want %v", c.path, got, c.want)
		}
	}
}

func TestGitHubManifestPageRefuses(t *testing.T) {
	e, _ := newGitHubEnv(t)
	rec := e.as("POST", "/api/github/apps", `{}`)
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	start := decode[panel.GitHubStart](t, rec)

	// Without a sign-in.
	rec = e.do(req{method: "GET", path: start.URL})
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "<form") || strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src") {
		t.Fatalf("signed out: %d %s", rec.Code, rec.Body)
	}
	// Another sign-in of the same admin.
	other := e.login("root", "root-password-1")
	rec = e.do(req{method: "GET", path: start.URL, cookie: other})
	if rec.Code != 400 || strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("other sign-in: %d %s", rec.Code, rec.Body)
	}
	// A member.
	if rec := e.as("POST", "/api/users", `{"name":"bob","password":"bob-password-1","role":"member"}`); rec.Code != 201 {
		t.Fatalf("member: %d %s", rec.Code, rec.Body)
	}
	bob := e.login("bob", "bob-password-1")
	rec = e.do(req{method: "GET", path: start.URL, cookie: bob})
	if rec.Code != 403 || strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("member: %d %s", rec.Code, rec.Body)
	}
	for _, r := range []req{
		{method: "GET", path: "/api/github", cookie: bob},
		{method: "POST", path: "/api/github/apps", body: `{}`, cookie: bob, csrf: true},
		{method: "POST", path: "/api/github/refresh", cookie: bob, csrf: true},
		{method: "DELETE", path: "/api/github/apps/1", cookie: bob, csrf: true},
	} {
		if rec := e.do(r); rec.Code != 403 {
			t.Errorf("member %s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
	// The address for deliveries is reserved but not open yet.
	if rec := e.do(req{method: "POST", path: "/api/github/webhook", body: `{}`}); rec.Code != 404 {
		t.Fatalf("webhook: %d", rec.Code)
	}
}

// login signs in and returns the new session's cookie.
func (e *env) login(name, password string) string {
	e.t.Helper()
	body, _ := json.Marshal(map[string]string{"name": name, "password": password})
	rec := e.do(req{method: "POST", path: "/api/login", body: string(body)})
	if rec.Code != 200 {
		e.t.Fatalf("login %s: %d %s", name, rec.Code, rec.Body)
	}
	return sessionCookie(e.t, rec)
}
