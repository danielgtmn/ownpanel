package panel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
	"github.com/danielgtmn/panzerschrank"
)

// Signed-in actors with their own sessions, as the browser flow needs.
var (
	rootSess  = withSession(admin, "root-session")
	rootOther = withSession(admin, "root-other-session")
	bobSess   = withSession(bob, "bob-session")
)

func withSession(a Actor, id string) Actor {
	a.sessionID = id
	return a
}

type githubWorld struct {
	world
	t    *testing.T
	gh   *githubtest.Server
	now  time.Time
	logs *bytes.Buffer
	// site and other are repositories of the organisation acme.
	site, other int64
}

func newGitHubWorld(t *testing.T) *githubWorld {
	t.Helper()
	g := &githubWorld{world: newWorld(t), t: t, gh: githubtest.New(), now: time.Now().UTC().Truncate(time.Second), logs: &bytes.Buffer{}}
	t.Cleanup(g.gh.Close)
	g.gh.Now = func() time.Time { return g.now }
	client, err := github.New(g.gh.URL, github.WithClock(func() time.Time { return g.now }))
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, panzerschrank.KeySize)
	rand.Read(key)
	box, err := panzerschrank.New(key)
	if err != nil {
		t.Fatal(err)
	}
	svc := g.svc
	svc.secrets = box
	svc.github = client
	svc.now = func() time.Time { return g.now }
	svc.opts.TLSMode = caddy.TLSInternal
	svc.opts.PanelDomain = "panel.example.com"
	g.site = g.gh.AddRepo("acme", "site", true)
	g.other = g.gh.AddRepo("acme", "other", false)
	g.gh.AddRepo("someone-else", "private", true)

	// Everything logged during the test is kept, to look for secrets.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(g.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return g
}

// noRedirects is a browser that stops at GitHub's redirect back.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// confirmOnGitHub does what the person's browser does with the manifest
// form: post it to GitHub and come back with the code and state.
func (g *githubWorld) confirmOnGitHub(form GitHubManifestForm) (code, state string) {
	g.t.Helper()
	resp, err := noRedirects.PostForm(form.Action, url.Values{"manifest": {form.Manifest}})
	if err != nil {
		g.t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		g.t.Fatalf("form answer: %d %v", resp.StatusCode, err)
	}
	if !strings.HasPrefix(loc.String(), "https://panel.example.com:8443"+GitHubCreatedPath+"?") {
		g.t.Fatalf("back to %s", loc)
	}
	return loc.Query().Get("code"), loc.Query().Get("state")
}

// installOnGitHub opens GitHub's installation page and returns where it
// sends the person back.
func (g *githubWorld) installOnGitHub(start GitHubStart) url.Values {
	g.t.Helper()
	resp, err := noRedirects.Get(start.URL)
	if err != nil {
		g.t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), "https://panel.example.com:8443"+GitHubInstalledPath+"?") {
		g.t.Fatalf("installation answer: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query()
}

// connect runs the whole flow as a for org and returns the connection.
func (g *githubWorld) connect(a Actor, org string) GitHubApp {
	g.t.Helper()
	ctx := context.Background()
	start, err := g.svc.StartGitHubApp(ctx, a, org)
	if err != nil {
		g.t.Fatal(err)
	}
	form, err := g.svc.GitHubManifestForm(ctx, a, stateOf(g.t, start.URL))
	if err != nil {
		g.t.Fatal(err)
	}
	code, state := g.confirmOnGitHub(form)
	app, err := g.svc.FinishGitHubApp(ctx, a, code, state)
	if err != nil {
		g.t.Fatal(err)
	}
	inst, err := g.svc.StartGitHubInstall(ctx, a, app.ID)
	if err != nil {
		g.t.Fatal(err)
	}
	back := g.installOnGitHub(inst)
	id, _ := strconv.ParseInt(back.Get("installation_id"), 10, 64)
	app, err = g.svc.FinishGitHubInstall(ctx, a, id, back.Get("state"))
	if err != nil {
		g.t.Fatal(err)
	}
	return app
}

func stateOf(t *testing.T, u string) string {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("state")
}

// secrets returns every secret of every App the fake created, and every
// token it handed out.
func (g *githubWorld) secrets() []string {
	var out []string
	for _, id := range g.gh.Apps() {
		app, _ := g.gh.App(id)
		out = append(out, app.WebhookSecret, app.ClientSecret, "PRIVATE KEY")
	}
	for _, tok := range g.gh.Tokens() {
		out = append(out, tok.Value)
	}
	for _, j := range g.gh.JWTs() {
		out = append(out, j.Raw)
	}
	return out
}

func (g *githubWorld) assertNoSecrets(what string, data string) {
	g.t.Helper()
	for _, s := range g.secrets() {
		if s != "" && strings.Contains(data, s) {
			g.t.Fatalf("%s contains a secret (%.12s…):\n%s", what, s, data)
		}
	}
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGitHubWholeFlow(t *testing.T) {
	g := newGitHubWorld(t)
	ctx := context.Background()
	var answers []string

	start, err := g.svc.StartGitHubApp(ctx, rootSess, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(start.URL, GitHubManifestPath+"?state=") {
		t.Fatalf("start = %s", start.URL)
	}
	state := stateOf(t, start.URL)
	form, err := g.svc.GitHubManifestForm(ctx, rootSess, state)
	if err != nil {
		t.Fatal(err)
	}
	if form.Action != g.gh.URL+"/organizations/acme/settings/apps/new?state="+state || form.Origin != g.gh.URL {
		t.Fatalf("form: %+v", form)
	}
	// Showing the form again (a reload) still works.
	if _, err := g.svc.GitHubManifestForm(ctx, rootSess, state); err != nil {
		t.Fatal(err)
	}
	var m githubtest.Manifest
	if err := json.Unmarshal([]byte(form.Manifest), &m); err != nil {
		t.Fatal(err)
	}
	base := "https://panel.example.com:8443"
	if m.Public || m.URL != base+"/" || m.HookAttributes.URL != base+GitHubWebhookPath || m.HookAttributes.Active == nil ||
		!*m.HookAttributes.Active || m.RedirectURL != base+GitHubCreatedPath || m.SetupURL != base+GitHubInstalledPath ||
		!m.SetupOnUpdate || len([]rune(m.Name)) > 34 || !strings.HasPrefix(m.Name, "ownpanel panel.example.com") {
		t.Fatalf("manifest: %+v", m)
	}
	wantPerms := map[string]string{"contents": "read", "metadata": "read", "statuses": "write", "pull_requests": "write"}
	if fmt.Sprint(m.DefaultPermissions) != fmt.Sprint(wantPerms) || !slices.Equal(m.DefaultEvents, []string{"push", "repository", "pull_request"}) {
		t.Fatalf("permissions %v, events %v", m.DefaultPermissions, m.DefaultEvents)
	}

	code, back := g.confirmOnGitHub(form)
	if back != state {
		t.Fatalf("state came back as %q", back)
	}
	app, err := g.svc.FinishGitHubApp(ctx, rootSess, code, state)
	if err != nil {
		t.Fatal(err)
	}
	answers = append(answers, asJSON(t, app))
	if app.Owner != "acme" || app.OwnerType != "organization" || len(app.Installations) != 0 ||
		app.SettingsURL != g.gh.URL+"/organizations/acme/settings/apps/"+app.Slug || app.CreatedBy != "root" {
		t.Fatalf("app: %+v", app)
	}

	// Stored sealed, with the panel's purposes.
	row, err := g.svc.store.GitHubApp(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	fake, _ := g.gh.App(app.AppID)
	for _, c := range []struct {
		sealed  []byte
		purpose string
		plain   string
	}{
		{row.PrivateKey, githubKeyPurpose, "-----BEGIN RSA PRIVATE KEY-----"},
		{row.WebhookSecret, githubWebhookPurpose, fake.WebhookSecret},
		{row.ClientSecret, githubClientPurpose, fake.ClientSecret},
	} {
		if bytes.Contains(c.sealed, []byte(c.plain)) {
			t.Fatalf("%s stored in the clear", c.purpose)
		}
		plain, err := g.svc.secrets.Open(c.sealed, c.purpose)
		if err != nil || !strings.HasPrefix(string(plain), c.plain) {
			t.Fatalf("%s: %v", c.purpose, err)
		}
	}

	// The code works once; so does the state.
	if _, err := g.svc.FinishGitHubApp(ctx, rootSess, code, state); KindOf(err) != Invalid {
		t.Fatalf("reused state: %v", err)
	}

	inst, err := g.svc.StartGitHubInstall(ctx, rootSess, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(inst.URL, g.gh.URL+"/apps/"+app.Slug+"/installations/new?state=") {
		t.Fatalf("install = %s", inst.URL)
	}
	q := g.installOnGitHub(inst)
	id, _ := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	app, err = g.svc.FinishGitHubInstall(ctx, rootSess, id, q.Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	answers = append(answers, asJSON(t, app))
	if len(app.Installations) != 1 || app.Installations[0].Account != "acme" || app.Installations[0].RepositoryCount == nil ||
		*app.Installations[0].RepositoryCount != 2 || app.Installations[0].RepositorySelection != "selected" {
		t.Fatalf("installed: %+v", app.Installations)
	}

	repos, err := g.svc.GitHubRepositories(ctx, rootSess, id)
	if err != nil {
		t.Fatal(err)
	}
	answers = append(answers, asJSON(t, repos))
	if len(repos.Repositories) != 2 || repos.Repositories[0].FullName != "acme/site" || repos.Truncated {
		t.Fatalf("repositories: %+v", repos)
	}
	if r, err := g.svc.UsableGitHubRepository(ctx, rootSess, id, g.site); err != nil || r.FullName != "acme/site" {
		t.Fatalf("usable: %+v %v", r, err)
	}
	if _, err := g.svc.UsableGitHubRepository(ctx, rootSess, id, 999999); KindOf(err) != NotFound {
		t.Fatalf("unknown repository: %v", err)
	}

	// A token for fetching code: one repository, read only.
	before := len(g.gh.Tokens())
	tok, err := g.svc.githubReadToken(ctx, id, g.site)
	if err != nil {
		t.Fatal(err)
	}
	issued := g.gh.Tokens()
	if len(issued) != before+1 {
		t.Fatalf("tokens: %d", len(issued))
	}
	last := issued[len(issued)-1]
	for _, it := range issued {
		if it.Value == tok.Value() {
			last = it
		}
	}
	if last.Value != tok.Value() || !slices.Equal(last.RepositoryIDs, []int64{g.site}) ||
		fmt.Sprint(last.Permissions) != fmt.Sprint(map[string]string{"contents": "read", "metadata": "read"}) {
		t.Fatalf("read token: %+v", last)
	}
	// Tokens for listing may only read metadata.
	for _, it := range issued {
		if it.Value != tok.Value() && fmt.Sprint(it.Permissions) != "map[metadata:read]" {
			t.Fatalf("listing token: %+v", it)
		}
	}
	// Every JWT was signed by the App's key (the fake checks), names the
	// client id and lives at most 10 minutes.
	jwts := g.gh.JWTs()
	if len(jwts) == 0 {
		t.Fatal("no JWT")
	}
	for _, j := range jwts {
		if j.Issuer != fake.ClientID || j.Expires.Sub(j.IssuedAt) > 10*time.Minute || j.IssuedAt.After(g.now) {
			t.Fatalf("JWT: %+v", j)
		}
	}
	if _, err := g.svc.UsableGitHubRepository(ctx, bobSess, id, g.site); KindOf(err) != NotFound {
		t.Fatalf("member: %v", err)
	}

	conns, err := g.svc.GitHubConnections(ctx, rootRead)
	if err != nil || !conns.Ready || len(conns.Apps) != 1 {
		t.Fatalf("connections: %+v %v", conns, err)
	}
	answers = append(answers, asJSON(t, conns))

	removed, err := g.svc.DeleteGitHubApp(ctx, rootWriteTok, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	answers = append(answers, asJSON(t, removed))
	if removed.SettingsURL != app.SettingsURL {
		t.Fatalf("removed: %+v", removed)
	}
	if apps, _ := g.svc.store.GitHubApps(ctx); len(apps) != 0 {
		t.Fatalf("left: %+v", apps)
	}
	if insts, _ := g.svc.store.GitHubInstallations(ctx, 0); len(insts) != 0 {
		t.Fatalf("installations left: %+v", insts)
	}
	if _, err := g.svc.GitHubRepositories(ctx, rootSess, id); KindOf(err) != NotFound {
		t.Fatalf("after removal: %v", err)
	}

	// No secret in an answer, the log or the activity log.
	g.assertNoSecrets("answers", strings.Join(answers, "\n"))
	g.assertNoSecrets("log", g.logs.String())
	if strings.Contains(strings.Join(answers, "\n")+g.logs.String(), code) {
		t.Fatal("the manifest code leaked")
	}
	acts, err := g.svc.store.Activities(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, a := range acts {
		actions = append(actions, a.Action)
		g.assertNoSecrets("activity", a.Detail+a.Target)
		if strings.Contains(a.Detail+a.Target, code) {
			t.Fatal("the manifest code is in the activity log")
		}
	}
	for _, want := range []string{"github.connect", "github.install", "github.disconnect"} {
		if !slices.Contains(actions, want) {
			t.Errorf("no %s in %v", want, actions)
		}
	}
}

func TestGitHubPersonalAccount(t *testing.T) {
	g := newGitHubWorld(t)
	g.gh.AddRepo(g.gh.User, "blog", false)
	app := g.connect(rootSess, "")
	if app.Owner != g.gh.User || app.OwnerType != "user" || len(app.Installations) != 1 ||
		app.SettingsURL != g.gh.URL+"/settings/apps/"+app.Slug {
		t.Fatalf("app: %+v", app)
	}
	// Another account gets its own App.
	other := g.connect(rootSess, "acme")
	if other.AppID == app.AppID {
		t.Fatal("same App for two accounts")
	}
	if _, err := g.svc.StartGitHubApp(context.Background(), rootSess, "ACME"); KindOf(err) != Conflict {
		t.Fatalf("connected twice: %v", err)
	}
}

func TestGitHubStateChecks(t *testing.T) {
	g := newGitHubWorld(t)
	ctx := context.Background()
	start := func(a Actor) (string, string) {
		s, err := g.svc.StartGitHubApp(ctx, a, "")
		if err != nil {
			t.Fatal(err)
		}
		state := stateOf(t, s.URL)
		form, err := g.svc.GitHubManifestForm(ctx, a, state)
		if err != nil {
			t.Fatal(err)
		}
		code, _ := g.confirmOnGitHub(form)
		return code, state
	}

	code, state := start(rootSess)
	for name, s := range map[string]string{
		"empty": "", "malformed": "x<script>", "unknown": strings.Repeat("A", 43), "too long": state + "A",
	} {
		if _, err := g.svc.FinishGitHubApp(ctx, rootSess, code, s); KindOf(err) != Invalid {
			t.Errorf("%s state: %v", name, err)
		}
	}
	// Another sign-in of the same person cannot use it, nor show the form.
	if _, err := g.svc.GitHubManifestForm(ctx, rootOther, state); KindOf(err) != Invalid {
		t.Errorf("form in other session: %v", err)
	}
	if _, err := g.svc.FinishGitHubApp(ctx, rootOther, code, state); KindOf(err) != Invalid {
		t.Errorf("other session: %v", err)
	}
	// ... and that did not use it up for the right one.
	if _, err := g.svc.FinishGitHubApp(ctx, rootSess, code, state); err != nil {
		t.Fatalf("own session after a foreign try: %v", err)
	}
	if _, err := g.svc.FinishGitHubApp(ctx, rootSess, code, state); KindOf(err) != Invalid {
		t.Errorf("reused: %v", err)
	}

	// Expired.
	code, state = start(rootSess)
	g.now = g.now.Add(githubFlowTTL + time.Second)
	if _, err := g.svc.FinishGitHubApp(ctx, rootSess, code, state); KindOf(err) != Invalid {
		t.Errorf("expired: %v", err)
	}

	// A state for installing is not one for creating, and the other way.
	g.now = time.Now().UTC().Truncate(time.Second)
	apps, _ := g.svc.store.GitHubApps(ctx)
	inst, err := g.svc.StartGitHubInstall(ctx, rootSess, apps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.svc.FinishGitHubApp(ctx, rootSess, code, stateOf(t, inst.URL)); KindOf(err) != Invalid {
		t.Errorf("install state for create: %v", err)
	}
	code, state = start(rootSess)
	if _, err := g.svc.FinishGitHubInstall(ctx, rootSess, 1, state); KindOf(err) != Invalid {
		t.Errorf("create state for install: %v", err)
	}
	// A bad code with a good state.
	if _, err := g.svc.FinishGitHubApp(ctx, rootSess, "nope", state); KindOf(err) != Invalid {
		t.Errorf("unknown code: %v", err)
	}
	if n := len(g.gh.Apps()); n != 3 {
		t.Errorf("fake has %d Apps", n)
	}
	if stored, _ := g.svc.store.GitHubApps(ctx); len(stored) != 1 {
		t.Errorf("stored %d Apps", len(stored))
	}
}

func TestGitHubForeignInstallation(t *testing.T) {
	g := newGitHubWorld(t)
	ctx := context.Background()
	mine := g.connect(rootSess, "acme")

	// An App the panel does not know, installed somewhere.
	code, _ := g.gh.CreateCode(githubtest.Manifest{URL: "https://x.example/", HookAttributes: struct {
		URL    string `json:"url"`
		Active *bool  `json:"active"`
	}{URL: "https://x.example/hook"}}, "evil")
	creds, err := g.svc.github.ConvertManifest(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	foreign := g.gh.AddInstallation(creds.ID, "evil", "Organization", nil)

	start, err := g.svc.StartGitHubInstall(ctx, rootSess, mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.svc.FinishGitHubInstall(ctx, rootSess, foreign, stateOf(t, start.URL)); KindOf(err) != NotFound {
		t.Fatalf("foreign installation: %v", err)
	}
	if _, err := g.svc.store.GitHubInstallation(ctx, foreign); KindOf(storeError(err)) != NotFound {
		t.Fatalf("foreign installation stored: %v", err)
	}
	if _, err := g.svc.GitHubRepositories(ctx, rootSess, foreign); KindOf(err) != NotFound {
		t.Fatalf("repositories of a foreign installation: %v", err)
	}
}

func TestGitHubRefresh(t *testing.T) {
	g := newGitHubWorld(t)
	ctx := context.Background()
	app := g.connect(rootSess, "acme")
	instID := app.Installations[0].ID

	// Installed again from GitHub's side, with all repositories.
	g.gh.RemoveInstallation(instID)
	newID := g.gh.AddInstallation(app.AppID, "acme", "Organization", nil)
	conns, err := g.svc.RefreshGitHub(ctx, rootWriteTok)
	if err != nil {
		t.Fatal(err)
	}
	got := conns.Apps[0]
	if len(got.Installations) != 1 || got.Installations[0].ID != newID || got.Installations[0].RepositorySelection != "all" ||
		got.LastError != "" || got.Installations[0].RepositoryCount == nil || *got.Installations[0].RepositoryCount != 2 {
		t.Fatalf("after refresh: %+v", got)
	}

	// The App was deleted on GitHub.
	g.gh.DeleteApp(app.AppID)
	conns, err = g.svc.RefreshGitHub(ctx, rootSess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conns.Apps[0].LastError, "deleted on GitHub") {
		t.Fatalf("problem: %q", conns.Apps[0].LastError)
	}
	g.assertNoSecrets("log", g.logs.String())
}

func TestGitHubNeedsPanelDomainWithHTTPS(t *testing.T) {
	g := newGitHubWorld(t)
	ctx := context.Background()
	g.svc.opts.PanelDomain = ""
	if _, err := g.svc.StartGitHubApp(ctx, rootSess, ""); KindOf(err) != Conflict || !strings.Contains(err.Error(), "panel domain") {
		t.Fatalf("no domain: %v", err)
	}
	conns, err := g.svc.GitHubConnections(ctx, rootSess)
	if err != nil || conns.Ready || !strings.Contains(conns.Problem, "own domain") {
		t.Fatalf("connections: %+v %v", conns, err)
	}
	g.svc.opts.PanelDomain = "panel.example.com"
	g.svc.opts.TLSMode = caddy.TLSOff
	if _, err := g.svc.StartGitHubApp(ctx, rootSess, ""); KindOf(err) != Conflict || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("no HTTPS: %v", err)
	}
	g.svc.opts.TLSMode = caddy.TLSACME
	if _, err := g.svc.StartGitHubApp(ctx, rootSess, "not a name"); KindOf(err) != Invalid {
		t.Fatalf("bad organisation: %v", err)
	}
	g.svc.secrets = nil
	if _, err := g.svc.StartGitHubApp(ctx, rootSess, ""); KindOf(err) != Internal {
		t.Fatalf("no secret key: %v", err)
	}
}

func TestGitHubPermissionMatrix(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", rootSess}, {"admin write token", rootWriteTok}, {"member", bobSess}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	type env struct {
		g     *githubWorld
		app   GitHubApp
		inst  int64
		state string // a fresh state of rootSess for each operation
	}
	ops := []struct {
		name string
		run  func(e env, a Actor) error
		want []Kind
	}{
		{"see connections", func(e env, a Actor) error { _, err := e.g.svc.GitHubConnections(ctx, a); return err },
			[]Kind{ok, ok, Forbidden, Forbidden, Forbidden, ok, Forbidden, Unauthenticated}},
		{"start connecting", func(e env, a Actor) error { _, err := e.g.svc.StartGitHubApp(ctx, a, ""); return err },
			[]Kind{ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"show the form", func(e env, a Actor) error { _, err := e.g.svc.GitHubManifestForm(ctx, a, e.state); return err },
			[]Kind{ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"finish connecting", func(e env, a Actor) error { _, err := e.g.svc.FinishGitHubApp(ctx, a, "code", e.state); return err },
			[]Kind{Invalid, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"start installing", func(e env, a Actor) error { _, err := e.g.svc.StartGitHubInstall(ctx, a, e.app.ID); return err },
			[]Kind{ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"finish installing", func(e env, a Actor) error { _, err := e.g.svc.FinishGitHubInstall(ctx, a, e.inst, e.state); return err },
			[]Kind{Invalid, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"refresh", func(e env, a Actor) error { _, err := e.g.svc.RefreshGitHub(ctx, a); return err },
			[]Kind{ok, ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
		{"list repositories", func(e env, a Actor) error { _, err := e.g.svc.GitHubRepositories(ctx, a, e.inst); return err },
			[]Kind{ok, ok, Forbidden, Forbidden, Forbidden, ok, Forbidden, Unauthenticated}},
		{"use a repository", func(e env, a Actor) error {
			_, err := e.g.svc.UsableGitHubRepository(ctx, a, e.inst, e.g.site)
			return err
		}, []Kind{ok, ok, NotFound, NotFound, NotFound, ok, NotFound, Unauthenticated}},
		{"remove", func(e env, a Actor) error { _, err := e.g.svc.DeleteGitHubApp(ctx, a, e.app.ID); return err },
			[]Kind{ok, ok, Forbidden, Forbidden, Forbidden, Forbidden, Forbidden, Unauthenticated}},
	}
	g := newGitHubWorld(t)
	app := g.connect(rootSess, "acme")
	snapshot := func() (int, int) {
		apps, _ := g.svc.store.GitHubApps(ctx)
		insts, _ := g.svc.store.GitHubInstallations(ctx, 0)
		return len(apps), len(insts)
	}
	for _, op := range ops {
		for i, actor := range actors {
			t.Run(op.name+"/"+actor.name, func(t *testing.T) {
				// Removing must start from a connected App every time.
				if apps, _ := snapshot(); apps == 0 {
					app = g.connect(rootSess, "acme")
				}
				g.svc.githubFlows.mu.Lock()
				clear(g.svc.githubFlows.m)
				g.svc.githubFlows.mu.Unlock()
				start, err := g.svc.StartGitHubApp(ctx, rootSess, "")
				if err != nil {
					t.Fatal(err)
				}
				e := env{g: g, app: app, inst: app.Installations[0].ID, state: stateOf(t, start.URL)}
				apps, insts := snapshot()
				err = op.run(e, actor.a)
				want := op.want[i]
				if want == ok {
					if err != nil {
						t.Fatalf("want success, got %v (%v)", err, KindOf(err))
					}
					return
				}
				wantKind(t, err, want)
				if a2, i2 := snapshot(); a2 != apps || i2 != insts {
					t.Fatalf("a refused %s changed the stored connections", op.name)
				}
			})
		}
	}
	// Members never see a name from a connection in an error.
	_, err := g.svc.UsableGitHubRepository(ctx, bobSess, app.Installations[0].ID, g.site)
	if err == nil || strings.Contains(err.Error(), "acme") {
		t.Fatalf("member error: %v", err)
	}
}

func TestGitHubFlowsAreLimited(t *testing.T) {
	g := newGitHubWorld(t)
	ctx := context.Background()
	for range githubMaxFlows {
		if _, err := g.svc.StartGitHubApp(ctx, rootSess, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.svc.StartGitHubApp(ctx, rootSess, ""); KindOf(err) != Throttled {
		t.Fatalf("one too many: %v", err)
	}
	// Expired ones make room again.
	g.now = g.now.Add(githubFlowTTL)
	if _, err := g.svc.StartGitHubApp(ctx, rootSess, ""); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubAppName(t *testing.T) {
	for _, d := range []string{"p.example.com", strings.Repeat("a", 60) + ".example.com"} {
		n := githubAppName(d)
		if len([]rune(n)) > githubAppNameMax || !strings.HasPrefix(n, "ownpanel ") {
			t.Errorf("%q -> %q", d, n)
		}
	}
	if githubAppName("p.example.com") == githubAppName("p.example.com") {
		t.Error("names of two Apps are the same")
	}
}
