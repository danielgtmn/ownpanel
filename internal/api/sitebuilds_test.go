package api

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build/buildtest"
	"github.com/danielgtmn/ownpanel/internal/panel"
)

func TestSiteBuildsOverHTTP(t *testing.T) {
	fake := buildtest.Start(t)
	e, gh := newGitHubEnv(t, fake.Client())
	repo := gh.AddRepo("acme", "web", false)
	inst := connectAcme(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.svc.RunBuilds(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	if rec := e.as("PUT", "/api/sites/blog", `{"domains":["blog.example.com"]}`); rec.Code != 200 {
		t.Fatalf("site: %d %s", rec.Code, rec.Body)
	}
	sha := gh.SetFiles(repo, "main", map[string]string{"index.html": "<h1>hi</h1>"})
	link := `{"installation_id":` + strconv.FormatInt(inst, 10) + `,"repository_id":` + strconv.FormatInt(repo, 10) + `,"branch":"main","publish_on_push":true}`
	if rec := e.as("PUT", "/api/sites/blog/source", link); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"publishing_available":true`) {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}

	// Settings: none yet, Build now asks for them.
	rec := e.as("GET", "/api/sites/blog/build-settings", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("POST", "/api/sites/blog/runs", ""); rec.Code != 409 {
		t.Fatalf("build without settings: %d %s", rec.Code, rec.Body)
	}
	rec = e.as("GET", "/api/sites/blog/build-settings/proposal", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"preset":"html"`) || !strings.Contains(rec.Body.String(), sha) {
		t.Fatalf("proposal: %d %s", rec.Code, rec.Body)
	}
	const secret = "tok_abcdefghijkl"
	rec = e.as("PUT", "/api/sites/blog/build-settings", `{"command":"","output":".","variables":[{"name":"TOKEN","value":"`+secret+`","secret":true}]}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), secret) || !strings.Contains(rec.Body.String(), `{"name":"TOKEN","secret":true}`) {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("PUT", "/api/sites/blog/build-settings", `{"output":"../x"}`); rec.Code != 400 {
		t.Fatalf("bad output: %d %s", rec.Code, rec.Body)
	}

	// Build now, then its run and log.
	rec = e.as("POST", "/api/sites/blog/runs", "")
	run := decode[panel.Run](t, rec)
	if rec.Code != 201 || run.Trigger != "manual" || run.Commit != sha {
		t.Fatalf("build now: %d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list := decode[panel.SiteRuns](t, e.as("GET", "/api/sites/blog/runs", ""))
		if len(list.Runs) > 0 && list.Runs[0].Status == "succeeded" {
			run = list.Runs[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Status != "succeeded" || run.Release == "" {
		t.Fatalf("run: %+v", run)
	}
	rec = e.as("GET", "/api/sites/blog/runs/"+run.ID+"/log?offset=0", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"done":true`) {
		t.Fatalf("log: %d %s", rec.Code, rec.Body)
	}
	if rec := e.as("GET", "/api/sites/blog/runs/nope/log", ""); rec.Code != 404 {
		t.Fatalf("unknown run: %d", rec.Code)
	}
	if rec := e.as("POST", "/api/sites/blog/runs/"+run.ID+"/cancel", ""); rec.Code != 409 {
		t.Fatalf("cancel ended: %d %s", rec.Code, rec.Body)
	}

	// A read-only token reads, but neither builds nor changes settings.
	_, read := e.createToken("read", "null")
	if rec := e.do(req{method: "GET", path: "/api/sites/blog/runs/" + run.ID + "/log", token: read}); rec.Code != 200 {
		t.Fatalf("read token log: %d", rec.Code)
	}
	for _, r := range []req{
		{method: "POST", path: "/api/sites/blog/runs", token: read},
		{method: "PUT", path: "/api/sites/blog/build-settings", body: `{"output":"."}`, token: read},
		{method: "GET", path: "/api/sites/blog/build-settings/proposal", token: read},
	} {
		if rec := e.do(r); rec.Code != 403 {
			t.Fatalf("read token %s %s: %d", r.method, r.path, rec.Code)
		}
	}
}
