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

	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
	"github.com/danielgtmn/ownpanel/internal/panel"
	"github.com/danielgtmn/ownpanel/internal/store"
	"github.com/danielgtmn/panzerschrank"
)

func TestGitHubTools(t *testing.T) {
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
	e := newEnvWith(t, "panel.example.com", func(c *panel.Config) {
		c.Caddy.TLSMode = caddy.TLSInternal
		c.Secrets = box
		c.GitHub = client
	})
	ctx := context.Background()

	cs := e.connect(t, e.read)
	var status githubStatusOutput
	ok(t, call_(t, cs, "get_github_status", nil), &status)
	if status.Connected || !status.CanConnect || len(status.Connections) != 0 {
		t.Fatalf("before: %+v", status)
	}

	// Connect in the "browser", as the web interface does.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start, err := e.svc.StartGitHubApp(ctx, e.admin, "acme")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.URL)
	form, err := e.svc.GitHubManifestForm(ctx, e.admin, u.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
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
	inst, err := e.svc.StartGitHubInstall(ctx, e.admin, app.ID)
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
	if _, err := e.svc.FinishGitHubInstall(ctx, e.admin, id, back.Query().Get("state")); err != nil {
		t.Fatal(err)
	}

	ok(t, call_(t, cs, "get_github_status", nil), &status)
	if !status.Connected || len(status.Connections) != 1 || status.Connections[0].Owner != "acme" ||
		len(status.Connections[0].Installations) != 1 || status.Connections[0].Installations[0].InstallationID != id {
		t.Fatalf("after: %+v", status)
	}
	var repos githubRepositoriesOutput
	ok(t, call_(t, cs, "list_github_repositories", map[string]any{"installation_id": id}), &repos)
	if len(repos.Repositories) != 1 || repos.Repositories[0].FullName != "acme/site" {
		t.Fatalf("repositories: %+v", repos)
	}
	refused(t, call_(t, cs, "list_github_repositories", map[string]any{"installation_id": 1}), "not found")

	// Members see nothing.
	if _, err := e.svc.CreateUser(ctx, e.admin, panel.NewUser{Name: "bob", Password: "bob-password-1", Role: store.RoleMember}); err != nil {
		t.Fatal(err)
	}
	_, bobSession, err := e.svc.Login(ctx, panel.Credentials{Name: "bob", Password: "bob-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	bobActor, err := e.svc.SessionActor(ctx, bobSession)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := e.svc.CreateToken(ctx, bobActor, "bob", "write", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	bcs := e.connect(t, tok.Secret)
	refused(t, call_(t, bcs, "get_github_status", nil), "only administrators")
	refused(t, call_(t, bcs, "list_github_repositories", map[string]any{"installation_id": id}), "only administrators")

	// Link a site to the repository, change it, unlink it.
	wcs := e.connect(t, e.write)
	var site siteOutput
	ok(t, call_(t, wcs, "create_site", map[string]any{"name": "blog", "domains": []string{"blog.example.com"}}), &site)
	var src siteSourceOutput
	ok(t, call_(t, wcs, "get_site_source", map[string]any{"site": "blog"}), &src)
	if src.Source != nil || src.PublishingAvailable {
		t.Fatalf("before linking: %+v", src)
	}
	var linkable linkableRepositoriesOutput
	ok(t, call_(t, wcs, "list_site_source_repositories", map[string]any{"site": "blog"}), &linkable)
	if !linkable.Connected || len(linkable.Repositories) != 1 || linkable.Repositories[0].FullName != "acme/site" {
		t.Fatalf("linkable: %+v", linkable)
	}
	r := linkable.Repositories[0]
	var branches panel.SourceBranches
	ok(t, call_(t, wcs, "list_site_source_branches", map[string]any{"site": "blog", "installation_id": r.InstallationID, "repository_id": r.RepositoryID}), &branches)
	if len(branches.Branches) != 1 || branches.Branches[0] != "main" {
		t.Fatalf("branches: %+v", branches)
	}
	link := map[string]any{"site": "blog", "installation_id": r.InstallationID, "repository_id": r.RepositoryID, "branch": "main", "publish_on_push": true}
	ok(t, call_(t, wcs, "link_site_source", link), &src)
	if src.Source == nil || src.Source.Repository != "acme/site" || src.Source.Status != "linked" || !src.Source.PublishOnPush {
		t.Fatalf("linked: %+v", src)
	}
	refused(t, call_(t, wcs, "link_site_source", with(link, "branch", "-x")), "not a valid branch name")
	refused(t, call_(t, wcs, "link_site_source", with(link, "branch", "gone")), "has no branch")
	refused(t, call_(t, cs, "link_site_source", link), "can only read")
	ok(t, call_(t, cs, "get_site_source", map[string]any{"site": "blog"}), &src)
	if src.Source == nil || src.Source.Branch != "main" {
		t.Fatalf("read token sees: %+v", src)
	}
	var unlinked unlinkSourceOutput
	ok(t, call_(t, wcs, "unlink_site_source", map[string]any{"site": "blog"}), &unlinked)
	refused(t, call_(t, wcs, "unlink_site_source", map[string]any{"site": "blog"}), "no source")
	// A member cannot link their site to the organisation's repository.
	ok(t, call_(t, bcs, "create_site", map[string]any{"name": "bobs", "domains": []string{"bobs.example.com"}}), &site)
	refused(t, call_(t, bcs, "link_site_source", with(link, "site", "bobs")), "not found")
	refused(t, call_(t, bcs, "get_site_source", map[string]any{"site": "blog"}), "not found")

	// No secret in any answer.
	fake, _ := gh.App(app.AppID)
	for _, res := range []any{status, repos, linkable, branches, src} {
		b, _ := json.Marshal(res)
		s := strings.ToLower(string(b))
		for _, secret := range []string{fake.WebhookSecret, fake.ClientSecret, "private key", "ghs_"} {
			if strings.Contains(s, strings.ToLower(secret)) {
				t.Fatalf("answer contains %q", secret)
			}
		}
	}
}
