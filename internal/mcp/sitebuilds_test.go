package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build/buildtest"
	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/panzerschrank"
)

func TestSiteBuildTools(t *testing.T) {
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	repo := gh.AddRepo("acme", "site", true)
	sha := gh.SetFiles(repo, "main", map[string]string{
		"package.json":      `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^6"}}`,
		"package-lock.json": "{}", "vite.config.js": "export default {}",
	})
	client, _ := github.New(gh.URL)
	key := make([]byte, panzerschrank.KeySize)
	rand.Read(key)
	box, _ := panzerschrank.New(key)
	fake := buildtest.Start(t)
	e := newEnvWith(t, "panel.example.com", func(c *panel.Config) {
		c.Caddy.TLSMode = caddy.TLSInternal
		c.Secrets = box
		c.GitHub = client
		c.Builds = fake.Client()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.svc.RunBuilds(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	// Connect acme as the web interface does and link a site.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start, _ := e.svc.StartGitHubApp(ctx, e.admin, "acme")
	u, _ := url.Parse(start.URL)
	form, _ := e.svc.GitHubManifestForm(ctx, e.admin, u.Query().Get("state"))
	resp, err := noFollow.PostForm(form.Action, url.Values{"manifest": {form.Manifest}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	app, err := e.svc.FinishGitHubApp(ctx, e.admin, back.Query().Get("code"), back.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	inst, _ := e.svc.StartGitHubInstall(ctx, e.admin, app.ID)
	resp, err = noFollow.Get(inst.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ = url.Parse(resp.Header.Get("Location"))
	id, _ := strconv.ParseInt(back.Query().Get("installation_id"), 10, 64)
	if _, err := e.svc.FinishGitHubInstall(ctx, e.admin, id, back.Query().Get("state")); err != nil {
		t.Fatal(err)
	}
	wcs := e.connect(t, e.write)
	rcs := e.connect(t, e.read)
	var site siteOutput
	ok(t, call_(t, wcs, "create_site", map[string]any{"name": "blog", "domains": []string{"blog.example.com"}}), &site)
	var src siteSourceOutput
	ok(t, call_(t, wcs, "link_site_source", map[string]any{"site": "blog", "installation_id": id, "repository_id": repo,
		"branch": "main", "publish_on_push": true}), &src)
	if !src.PublishingAvailable {
		t.Fatalf("source: %+v", src)
	}

	// Settings: a proposal, saving, reading back without the secret.
	var prop buildProposalOutput
	ok(t, call_(t, wcs, "propose_site_build_settings", map[string]any{"site": "blog"}), &prop)
	if prop.Preset != "vite" || prop.Output != "dist" || prop.Commit != sha || prop.Command == "" {
		t.Fatalf("proposal: %+v", prop)
	}
	refused(t, call_(t, rcs, "propose_site_build_settings", map[string]any{"site": "blog"}), "can only read")
	refused(t, call_(t, wcs, "start_site_build", map[string]any{"site": "blog"}), "build settings")
	const secret = "mcp-secret-value-123"
	var settings buildSettingsOutput
	ok(t, call_(t, wcs, "set_site_build_settings", map[string]any{"site": "blog", "command": prop.Command, "output": prop.Output,
		"image": prop.Image, "variables": []map[string]any{{"name": "API_KEY", "value": secret, "secret": true}}}), &settings)
	if !settings.Configured || len(settings.Variables) != 1 || settings.Variables[0].Value != "" {
		t.Fatalf("settings: %+v", settings)
	}
	refused(t, call_(t, rcs, "set_site_build_settings", map[string]any{"site": "blog", "output": "."}), "can only read")
	ok(t, call_(t, rcs, "get_site_build_settings", map[string]any{"site": "blog"}), &settings)

	// Build now, follow the run, read its log.
	var run runInfo
	refused(t, call_(t, rcs, "start_site_build", map[string]any{"site": "blog"}), "can only read")
	ok(t, call_(t, wcs, "start_site_build", map[string]any{"site": "blog"}), &run)
	if run.Trigger != "manual" || run.Commit != sha {
		t.Fatalf("run: %+v", run)
	}
	var runs siteRunsOutput
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		ok(t, call_(t, rcs, "list_site_runs", map[string]any{"site": "blog"}), &runs)
		if len(runs.Runs) > 0 && runs.Runs[0].Status == "succeeded" {
			break
		}
	}
	if len(runs.Runs) == 0 || runs.Runs[0].Status != "succeeded" || runs.Runs[0].Release == "" {
		t.Fatalf("runs: %+v", runs)
	}
	var lg runLogOutput
	ok(t, call_(t, rcs, "read_site_run_log", map[string]any{"site": "blog", "run_id": run.ID}), &lg)
	if !lg.Done || lg.Live {
		t.Fatalf("log: %+v", lg)
	}
	refused(t, call_(t, wcs, "cancel_site_run", map[string]any{"site": "blog", "run_id": run.ID}), "already ended")
	refused(t, call_(t, rcs, "read_site_run_log", map[string]any{"site": "blog", "run_id": "nope"}), "not found")

	for _, v := range []any{prop, settings, run, runs, lg} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), secret) || strings.Contains(string(b), "ghs_") {
			t.Fatalf("an answer leaks: %s", b)
		}
	}
}
