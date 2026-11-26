package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// connectAcme connects the fake's organisation acme as root and returns
// the installation id.
func connectAcme(t *testing.T, e *env) int64 {
	t.Helper()
	ctx := context.Background()
	a := rootActor(t, e)
	start, err := e.svc.StartGitHubApp(ctx, a, "acme")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.URL)
	form, err := e.svc.GitHubManifestForm(ctx, a, u.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := noFollow.PostForm(form.Action, url.Values{"manifest": {form.Manifest}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	app, err := e.svc.FinishGitHubApp(ctx, a, back.Query().Get("code"), back.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := e.svc.StartGitHubInstall(ctx, a, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = noFollow.Get(inst.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ = url.Parse(resp.Header.Get("Location"))
	id, _ := strconv.ParseInt(back.Query().Get("installation_id"), 10, 64)
	if _, err := e.svc.FinishGitHubInstall(ctx, a, id, back.Query().Get("state")); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSiteSourceOverHTTP(t *testing.T) {
	e, gh := newGitHubEnv(t)
	inst := connectAcme(t, e)
	if rec := e.as("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`); rec.Code != 200 {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}

	rec := e.as("GET", "/api/sites/blog/source", "")
	if rec.Code != 200 || rec.Body.String() != `{"site":"blog","source":null,"publishing_available":false,"last_run":null}`+"\n" {
		t.Fatalf("no source: %d %q", rec.Code, rec.Body)
	}
	rec = e.as("GET", "/api/sites/blog/source/repositories", "")
	repos := decode[panel.LinkableRepositories](t, rec)
	if rec.Code != 200 || !repos.Connected || len(repos.Repositories) != 1 || repos.Repositories[0].FullName != "acme/site" {
		t.Fatalf("repositories: %d %s", rec.Code, rec.Body)
	}
	repo := repos.Repositories[0]
	q := "?installation_id=" + strconv.FormatInt(inst, 10) + "&repository_id=" + strconv.FormatInt(repo.ID, 10)
	rec = e.as("GET", "/api/sites/blog/source/branches"+q, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"default_branch":"main"`) {
		t.Fatalf("branches: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("GET", "/api/sites/blog/source/branches?installation_id=x&repository_id=1", ""); rec.Code != 404 {
		t.Fatalf("bad ids: %d %s", rec.Code, rec.Body)
	}

	link := `{"installation_id":` + strconv.FormatInt(inst, 10) + `,"repository_id":` + strconv.FormatInt(repo.ID, 10)
	if rec := e.as("PUT", "/api/sites/blog/source", link+`,"branch":"../x"}`); rec.Code != 400 {
		t.Fatalf("bad branch: %d %s", rec.Code, rec.Body)
	}
	rec = e.as("PUT", "/api/sites/blog/source", link+`,"branch":"main","publish_on_push":true}`)
	if rec.Code != 200 {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	st := decode[panel.SiteSourceState](t, rec)
	if st.Source == nil || st.Source.Repository != "acme/site" || !st.Source.PublishOnPush || st.Source.Status != "linked" {
		t.Fatalf("linked: %s", rec.Body)
	}
	bodies := rec.Body.String()
	rec = e.as("GET", "/api/sites/blog/source", "")
	bodies += rec.Body.String()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"branch":"main"`) {
		t.Fatalf("shown: %d %s", rec.Code, rec.Body)
	}

	// A read-only token sees it but cannot change it.
	_, read := e.createToken("read", "null")
	if rec := e.do(req{method: "GET", path: "/api/sites/blog/source", token: read}); rec.Code != 200 {
		t.Fatalf("read token: %d", rec.Code)
	}
	if rec := e.do(req{method: "DELETE", path: "/api/sites/blog/source", token: read}); rec.Code != 403 {
		t.Fatalf("read token unlinks: %d", rec.Code)
	}
	// A token limited to another site does not see it.
	if rec := e.as("PUT", "/api/sites/other", `{"domains":["other.example.com"]}`); rec.Code != 200 {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}
	_, limited := e.createToken("write", `["other"]`)
	for _, r := range []req{
		{method: "GET", path: "/api/sites/blog/source", token: limited},
		{method: "PUT", path: "/api/sites/blog/source", body: link + `,"branch":"main"}`, token: limited},
		{method: "DELETE", path: "/api/sites/blog/source", token: limited},
	} {
		if rec := e.do(r); rec.Code != 404 {
			t.Fatalf("limited token %s: %d %s", r.method, rec.Code, rec.Body)
		}
	}
	// A member who owns a site cannot link it to the organisation's
	// repository.
	if rec := e.as("POST", "/api/users", `{"name":"bob","password":"bob-password-1","role":"member"}`); rec.Code != 201 {
		t.Fatalf("member: %d %s", rec.Code, rec.Body)
	}
	bob := e.login("bob", "bob-password-1")
	if rec := e.do(req{method: "PUT", path: "/api/sites/bobs", body: `{"domains":["bobs.example.com"]}`, cookie: bob, csrf: true}); rec.Code != 200 {
		t.Fatalf("bob's site: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(req{method: "PUT", path: "/api/sites/bobs/source", body: link + `,"branch":"main"}`, cookie: bob, csrf: true})
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "acme") {
		t.Fatalf("member links: %d %s", rec.Code, rec.Body)
	}
	rec = e.do(req{method: "GET", path: "/api/sites/bobs/source/repositories", cookie: bob})
	if rec.Code != 200 || rec.Body.String() != `{"connected":false,"repositories":[],"problems":[],"truncated":false}`+"\n" {
		t.Fatalf("member's list: %d %s", rec.Code, rec.Body)
	}

	if rec := e.as("DELETE", "/api/sites/blog/source", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("unlink: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("DELETE", "/api/sites/blog/source", ""); rec.Code != 404 {
		t.Fatalf("unlink again: %d %s", rec.Code, rec.Body)
	}
	for _, tok := range gh.Tokens() {
		if strings.Contains(bodies, tok.Value) {
			t.Fatal("an answer contains a token")
		}
	}
}
