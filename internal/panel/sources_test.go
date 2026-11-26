package panel

import (
	"context"
	"strings"
	"testing"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// linkedWorld is a GitHub world with acme connected; inst is its
// installation.
func linkedWorld(t *testing.T) (*githubWorld, GitHubApp, int64) {
	t.Helper()
	g := newGitHubWorld(t)
	app := g.connect(rootSess, "acme")
	return g, app, app.Installations[0].ID
}

func (g *githubWorld) source(site string) *SiteSource {
	g.t.Helper()
	st, err := g.svc.GetSiteSource(context.Background(), rootSess, site)
	if err != nil {
		g.t.Fatal(err)
	}
	if st.PublishingAvailable {
		g.t.Fatal("publishing on push is not available yet")
	}
	return st.Source
}

func TestSiteSourceLinkChangeUnlink(t *testing.T) {
	g, _, inst := linkedWorld(t)
	ctx := context.Background()

	if src := g.source("rootsite"); src != nil {
		t.Fatalf("a new site has a source: %+v", src)
	}

	st, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main", PublishOnPush: true})
	if err != nil {
		t.Fatal(err)
	}
	src := st.Source
	if src == nil || src.Kind != "github" || src.Repository != "acme/site" || src.RepositoryID != g.site || src.Branch != "main" ||
		!src.PublishOnPush || src.Status != SourceLinked || src.Account != "acme" || src.LinkedBy != "root" || src.URL == "" {
		t.Fatalf("linked: %+v", src)
	}
	if got := g.source("rootsite"); got == nil || got.Status != SourceLinked || got.Repository != "acme/site" || got.URL != src.URL {
		t.Fatalf("shown: %+v", got)
	}

	// Change the branch and turn publishing on push off.
	g.gh.AddBranch(g.site, "release/2.0")
	if _, err := g.svc.SetSiteSource(ctx, rootWriteTok, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "release/2.0"}); err != nil {
		t.Fatal(err)
	}
	if got := g.source("rootsite"); got.Branch != "release/2.0" || got.PublishOnPush {
		t.Fatalf("changed: %+v", got)
	}

	// Another repository of the same installation.
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.other, Branch: "main", PublishOnPush: true}); err != nil {
		t.Fatal(err)
	}
	if got := g.source("rootsite"); got.Repository != "acme/other" {
		t.Fatalf("other repository: %+v", got)
	}

	// A branch that does not exist.
	_, err = g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "nope"})
	wantKind(t, err, Invalid)
	if !strings.Contains(err.Error(), "acme/site has no branch") {
		t.Fatalf("message: %v", err)
	}
	if got := g.source("rootsite"); got.Repository != "acme/other" {
		t.Fatalf("a refused change was stored: %+v", got)
	}

	branches, err := g.svc.SourceBranches(ctx, rootSess, "rootsite", inst, g.site)
	if err != nil || branches.DefaultBranch != "main" || strings.Join(branches.Branches, ",") != "main,release/2.0" {
		t.Fatalf("branches: %+v %v", branches, err)
	}
	// The token used for it could read that repository only.
	for _, tok := range g.gh.Tokens() {
		if tok.Permissions["contents"] != "" && (len(tok.RepositoryIDs) != 1 || tok.Permissions["contents"] != "read") {
			t.Fatalf("token too wide: %+v", tok.RepositoryIDs)
		}
	}

	if err := g.svc.RemoveSiteSource(ctx, rootSess, "rootsite"); err != nil {
		t.Fatal(err)
	}
	if src := g.source("rootsite"); src != nil {
		t.Fatalf("still linked: %+v", src)
	}
	wantKind(t, g.svc.RemoveSiteSource(ctx, rootSess, "rootsite"), NotFound)

	acts, err := g.svc.store.Activities(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var log []string
	for _, a := range acts {
		if strings.HasPrefix(a.Action, "site.source") {
			log = append(log, a.Action+": "+a.Detail)
		}
		g.assertNoSecrets("activity", a.Detail)
	}
	want := []string{
		"site.source-remove: acme/other",
		"site.source: acme/other, branch main, publish on push on",
		"site.source: acme/site, branch release/2.0, publish on push off",
		"site.source: acme/site, branch main, publish on push on",
	}
	if strings.Join(log, "\n") != strings.Join(want, "\n") {
		t.Fatalf("activity:\n%s", strings.Join(log, "\n"))
	}
	g.assertNoSecrets("log", g.logs.String())
}

func TestSiteSourceMemberCannotUseOrganisationRepositories(t *testing.T) {
	g, _, inst := linkedWorld(t)
	ctx := context.Background()

	// bob owns bobsite, but the repositories belong to the administrators.
	_, err := g.svc.SetSiteSource(ctx, bobSess, "bobsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main"})
	wantKind(t, err, NotFound)
	if strings.Contains(err.Error(), "acme") {
		t.Fatalf("names leak: %v", err)
	}
	list, err := g.svc.LinkableRepositories(ctx, bobSess, "bobsite")
	if err != nil || list.Connected || len(list.Repositories) != 0 {
		t.Fatalf("member's list: %+v %v", list, err)
	}
	_, err = g.svc.SourceBranches(ctx, bobSess, "bobsite", inst, g.site)
	wantKind(t, err, NotFound)

	// An administrator links bob's site; bob sees it and may unlink it,
	// but not change it.
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "bobsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	st, err := g.svc.GetSiteSource(ctx, bobSess, "bobsite")
	if err != nil || st.Source == nil || st.Source.Repository != "acme/site" || st.Source.Status != SourceLinked {
		t.Fatalf("bob sees: %+v %v", st, err)
	}
	_, err = g.svc.SetSiteSource(ctx, bobSess, "bobsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main", PublishOnPush: true})
	wantKind(t, err, NotFound)
	if err := g.svc.RemoveSiteSource(ctx, bobSess, "bobsite"); err != nil {
		t.Fatal(err)
	}

	// The administrator's list.
	list, err = g.svc.LinkableRepositories(ctx, rootSess, "bobsite")
	if err != nil || !list.Connected || len(list.Repositories) != 2 || list.Repositories[0].Account != "acme" {
		t.Fatalf("admin's list: %+v %v", list, err)
	}
}

func TestSiteSourcePermissionMatrix(t *testing.T) {
	ctx := context.Background()
	actors := []struct {
		name string
		a    Actor
	}{
		{"admin", rootSess}, {"admin write token", rootWriteTok}, {"member", bobSess}, {"member read token", bobRead},
		{"member site token", bobLimited}, {"admin read token", rootRead}, {"admin site token", rootLimited},
		{"nobody", nobody},
	}
	g, _, inst := linkedWorld(t)
	link := SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main"}
	ops := []struct {
		name string
		site string
		run  func(a Actor, site string) error
		want []Kind
	}{
		{"see", "bobsite", func(a Actor, site string) error { _, err := g.svc.GetSiteSource(ctx, a, site); return err },
			[]Kind{ok, ok, ok, ok, ok, ok, NotFound, Unauthenticated}},
		{"see another site", "bobother", func(a Actor, site string) error { _, err := g.svc.GetSiteSource(ctx, a, site); return err },
			[]Kind{ok, ok, ok, ok, NotFound, ok, NotFound, Unauthenticated}},
		{"link", "bobsite", func(a Actor, site string) error { _, err := g.svc.SetSiteSource(ctx, a, site, link); return err },
			[]Kind{ok, ok, NotFound, Forbidden, NotFound, Forbidden, NotFound, Unauthenticated}},
		{"link another site", "bobother", func(a Actor, site string) error { _, err := g.svc.SetSiteSource(ctx, a, site, link); return err },
			[]Kind{ok, ok, NotFound, Forbidden, NotFound, Forbidden, NotFound, Unauthenticated}},
		{"link the admin's site", "rootsite", func(a Actor, site string) error { _, err := g.svc.SetSiteSource(ctx, a, site, link); return err },
			[]Kind{ok, ok, NotFound, NotFound, NotFound, Forbidden, NotFound, Unauthenticated}},
		{"unlink", "bobsite", func(a Actor, site string) error { return g.svc.RemoveSiteSource(ctx, a, site) },
			[]Kind{ok, ok, ok, Forbidden, ok, Forbidden, NotFound, Unauthenticated}},
		{"unlink another site", "bobother", func(a Actor, site string) error { return g.svc.RemoveSiteSource(ctx, a, site) },
			[]Kind{ok, ok, ok, Forbidden, NotFound, Forbidden, NotFound, Unauthenticated}},
		{"list repositories", "bobsite", func(a Actor, site string) error { _, err := g.svc.LinkableRepositories(ctx, a, site); return err },
			[]Kind{ok, ok, ok, ok, ok, ok, NotFound, Unauthenticated}},
		{"list branches", "bobsite", func(a Actor, site string) error {
			_, err := g.svc.SourceBranches(ctx, a, site, inst, g.site)
			return err
		}, []Kind{ok, ok, NotFound, NotFound, NotFound, ok, NotFound, Unauthenticated}},
	}
	for _, op := range ops {
		for i, actor := range actors {
			t.Run(op.name+"/"+actor.name, func(t *testing.T) {
				// Every operation starts from a linked site.
				if _, err := g.svc.SetSiteSource(ctx, rootSess, op.site, SiteSourceInput{InstallationID: inst, RepositoryID: g.other, Branch: "main"}); err != nil {
					t.Fatal(err)
				}
				err := op.run(actor.a, op.site)
				if want := op.want[i]; want != ok {
					wantKind(t, err, want)
					if src := g.source(op.site); src == nil || src.Repository != "acme/other" {
						t.Fatalf("a refused %s changed the source: %+v", op.name, src)
					}
					return
				}
				if err != nil {
					t.Fatalf("want success, got %v (%v)", err, KindOf(err))
				}
			})
		}
	}
}

func TestSiteSourceBranchNames(t *testing.T) {
	g, _, inst := linkedWorld(t)
	ctx := context.Background()
	bad := []string{
		"", "-main", "a..b", "a b", "main.lock", "a/b.lock/c", ".hidden", "a/.b", "a//b", "/a", "a/", "a.", "a~1", "a^",
		"a:b", "a?", "a*", "a[0]", `a\b`, "@", "HEAD", "a@{1}", "a\x00b", "a\tb", "a\x7fb", "zero\u200bwidth",
		strings.Repeat("a", 256), "\xff",
	}
	for _, b := range bad {
		_, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: b})
		if KindOf(err) != Invalid {
			t.Errorf("branch %q: %v", b, err)
		}
	}
	for _, b := range []string{"main", "feature/x", "release-1.0", "v2_beta", "ünïcode", "a@b", strings.Repeat("a", 255)} {
		if err := ValidateBranch(b); err != nil {
			t.Errorf("branch %q refused: %v", b, err)
		}
	}
	// Nothing invalid reached GitHub.
	for _, r := range g.gh.Requests() {
		if strings.Contains(r, "/branches") {
			t.Fatalf("asked GitHub: %s", r)
		}
	}
	if src := g.source("rootsite"); src != nil {
		t.Fatalf("linked: %+v", src)
	}
}

func TestSiteSourceRenamedRepository(t *testing.T) {
	g, _, inst := linkedWorld(t)
	ctx := context.Background()
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	g.gh.RenameRepo(g.site, "website")
	g.svc.githubRepos.clear()
	src := g.source("rootsite")
	if src.Repository != "acme/website" || src.Status != SourceLinked || !strings.HasSuffix(src.URL, "/acme/website") {
		t.Fatalf("after the rename: %+v", src)
	}
	stored, err := g.svc.store.SiteSource(ctx, "rootsite")
	if err != nil || stored.Repository != "acme/website" || stored.RepositoryID != g.site {
		t.Fatalf("stored: %+v %v", stored, err)
	}
	// Changing the branch keeps working under the new name.
	g.gh.AddBranch(g.site, "dev")
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "dev"}); err != nil {
		t.Fatal(err)
	}
}

func TestSiteSourceRepositoryLeftTheInstallation(t *testing.T) {
	g, _, inst := linkedWorld(t)
	ctx := context.Background()
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	// On GitHub, the installation now sees only the other repository.
	g.gh.SetInstallationRepos(inst, []int64{g.other})
	g.svc.githubRepos.clear()
	if src := g.source("rootsite"); src.Status != SourceRepositoryMissing || src.Repository != "acme/site" {
		t.Fatalf("shown: %+v", src)
	}
	_, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main"})
	wantKind(t, err, NotFound)

	// A repository of another account, sent with this installation.
	g.gh.SetInstallationRepos(inst, nil)
	g.svc.githubRepos.clear()
	list, _ := g.svc.LinkableRepositories(ctx, rootSess, "rootsite")
	for _, r := range list.Repositories {
		if r.Owner != "acme" {
			t.Fatalf("listed %s", r.FullName)
		}
	}
	foreign := g.other + 1 // someone-else/private, added right after it
	_, err = g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: foreign, Branch: "main"})
	wantKind(t, err, NotFound)
}

func TestSiteSourceConnectionRemoved(t *testing.T) {
	g, app, inst := linkedWorld(t)
	ctx := context.Background()
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main", PublishOnPush: true}); err != nil {
		t.Fatal(err)
	}
	// The settings list the site under its installation.
	conns, err := g.svc.GitHubConnections(ctx, rootSess)
	if err != nil {
		t.Fatal(err)
	}
	if sites := conns.Apps[0].Installations[0].Sites; len(sites) != 1 || sites[0] != (GitHubLinkedSite{Site: "rootsite", Repository: "acme/site", RepositoryID: g.site, Branch: "main"}) ||
		len(conns.SitesWithoutConnection) != 0 {
		t.Fatalf("linked sites: %+v", conns)
	}

	// Uninstalled on GitHub, noticed by a refresh.
	g.gh.RemoveInstallation(inst)
	conns, err = g.svc.RefreshGitHub(ctx, rootSess)
	if err != nil {
		t.Fatal(err)
	}
	if len(conns.Apps[0].Installations) != 0 || len(conns.SitesWithoutConnection) != 1 || conns.SitesWithoutConnection[0].Site != "rootsite" {
		t.Fatalf("sites without connection: %+v", conns)
	}
	src := g.source("rootsite")
	if src.Status != SourceConnectionMissing || src.Repository != "acme/site" || src.Branch != "main" || src.InstallationID != 0 {
		t.Fatalf("after uninstalling: %+v", src)
	}

	// Installed again: link again, then remove the whole connection.
	newInst := g.gh.AddInstallation(app.AppID, "acme", "Organization", nil)
	if _, err := g.svc.RefreshGitHub(ctx, rootSess); err != nil {
		t.Fatal(err)
	}
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: newInst, RepositoryID: g.site, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.svc.DeleteGitHubApp(ctx, rootSess, app.ID); err != nil {
		t.Fatal(err)
	}
	src = g.source("rootsite")
	if src.Status != SourceConnectionMissing || src.Repository != "acme/site" {
		t.Fatalf("after removing the connection: %+v", src)
	}
	// Members see the same.
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "bobsite", SiteSourceInput{InstallationID: newInst, RepositoryID: g.site, Branch: "main"}); KindOf(err) != NotFound {
		t.Fatalf("linking to a removed connection: %v", err)
	}
	// It can still be unlinked.
	if err := g.svc.RemoveSiteSource(ctx, rootSess, "rootsite"); err != nil {
		t.Fatal(err)
	}
}

func TestSiteSourceGoesWithTheSite(t *testing.T) {
	g, _, inst := linkedWorld(t)
	ctx := context.Background()
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "bobother", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	// The test's web server cannot be updated with a panel domain; the
	// site is removed from the store all the same.
	if err := g.svc.DeleteSite(ctx, rootSess, "bobother"); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	if _, err := g.svc.store.SiteSource(ctx, "bobother"); KindOf(storeError(err)) != NotFound {
		t.Fatalf("source left: %v", err)
	}
	// A new site of the same name starts without one.
	if _, err := g.svc.SaveSite(ctx, bobSess, "bobother", []string{"bobother.example.com"}); err != nil && KindOf(err) != NotSynced {
		t.Fatal(err)
	}
	if src := g.source("bobother"); src != nil {
		t.Fatalf("inherited: %+v", src)
	}
	if list, err := g.svc.store.SiteSourcesOfRepository(ctx, g.site); err != nil || len(list) != 0 {
		t.Fatalf("by repository: %+v %v", list, err)
	}
}

func TestSiteSourceSuspendedInstallation(t *testing.T) {
	g, app, inst := linkedWorld(t)
	ctx := context.Background()
	if _, err := g.svc.SetSiteSource(ctx, rootSess, "rootsite", SiteSourceInput{InstallationID: inst, RepositoryID: g.site, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := g.svc.store.PutGitHubInstallation(ctx, store.GitHubInstallation{ID: inst, AppID: app.ID, AccountLogin: "acme",
		AccountType: "Organization", RepositorySelection: "all", Suspended: true}); err != nil {
		t.Fatal(err)
	}
	if src := g.source("rootsite"); src.Status != SourceSuspended {
		t.Fatalf("shown: %+v", src)
	}
}
