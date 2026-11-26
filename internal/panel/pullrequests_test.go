package panel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/build/buildtest"
	"github.com/danielgtmn/ownpanel/internal/caddy"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
)

// previewWorld is a build world with previews on addresses and rootsite
// configured; acme/site is private.
func newPreviewWorld(t *testing.T) *buildWorld {
	t.Helper()
	b := newBuildWorld(t)
	b.svc.opts.PanelUpstream = "127.0.0.1:8600"
	b.svc.opts.TLSMode = caddy.TLSInternal
	if _, err := b.svc.UpdateSettings(context.Background(), rootSess, SettingsUpdate{PreviewDomain: str("preview.example.net")}); err != nil {
		t.Fatal(err)
	}
	b.configure(rootSess, "rootsite")
	return b
}

func (b *buildWorld) pull(action string, repo int64, n int) []byte {
	return b.gh.PullPayload(action, repo, n, b.inst)
}

// eventually waits until cond holds.
func (b *buildWorld) eventually(what string, cond func() bool) {
	b.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	b.t.Fatalf("%s did not happen", what)
}

// botComment is the panel's comment about site on a pull request.
func (b *buildWorld) botComment(repo int64, n int, site string) (githubtest.Comment, int) {
	var found githubtest.Comment
	count := 0
	for _, c := range b.gh.Comments(repo, n) {
		if c.UserType == "Bot" && strings.Contains(c.Body, previewMarker(site)) {
			found = c
			count++
		}
	}
	return found, count
}

func (b *buildWorld) specOf(site, sha string) build.Spec {
	b.t.Helper()
	// The fake build service does not know commits: find the spec of the
	// newest build of site.
	var spec build.Spec
	for _, s := range b.fake.Specs() {
		if s.Site == site {
			spec = s
		}
	}
	return spec
}

func TestPullRequestPreviewLifecycle(t *testing.T) {
	b := newPreviewWorld(t)
	ctx := context.Background()
	live := b.waitRun("rootsite", b.pushCommit(map[string]string{"index.html": "live"}))
	if live.Status != RunSucceeded {
		t.Fatalf("live: %+v", live)
	}

	// A pull request against main: both linked sites (previews are on by
	// default) get a run; rootsite builds a version that is not live.
	shaA := b.gh.SetFiles(b.site, "feature", map[string]string{"index.html": "A"})
	n := b.gh.OpenPull(b.site, "main", "feature", 0, shaA, "Add a <b>page</b>", "ann")
	b.mustReceive(b.delivery("pull_request", b.pull("opened", b.site, n)), DeliveryHandled, 2)
	runA := b.waitRun("rootsite", shaA)
	if runA.Status != RunSucceeded || runA.Trigger != RunTriggerPullRequest || runA.PullRequest != n || runA.Fork ||
		runA.PullRequestURL != b.gh.URL+"/acme/site/pull/1" || runA.Message != "Add a <b>page</b>" || runA.Ref != "refs/pull/1/head" {
		t.Fatalf("run: %+v", runA)
	}
	if cur := b.svc.deploys.Current("rootsite"); cur != live.Release {
		t.Fatalf("a pull request changed the live version: %s", cur)
	}
	if !strings.HasPrefix(runA.PreviewURL, "https://") || !strings.Contains(runA.PreviewURL, "ownpanel-preview=") {
		t.Fatalf("preview url: %q", runA.PreviewURL)
	}
	if spec := b.specOf("rootsite", shaA); spec.Env["MODE"] != "production" || spec.Secrets["API_KEY"] != buildSecret || spec.Untrusted {
		t.Fatalf("a pull request from the same repository is built like a push: %+v", spec.Env)
	}
	site, _ := b.svc.Site(ctx, rootSess, "rootsite")
	var previews int
	for _, v := range site.Versions {
		if v.PullRequest == n {
			previews++
			if v.ID != runA.Release || v.Live || !v.Pending {
				t.Errorf("version: %+v", v)
			}
		}
	}
	if previews != 1 {
		t.Fatalf("versions: %+v", site.Versions)
	}

	// GitHub: a status with the preview as its target (the repository is
	// private) and one comment with the link.
	b.eventually("the comment", func() bool {
		c, _ := b.botComment(b.site, n, "rootsite")
		return strings.Contains(c.Body, runA.PreviewURL)
	})
	c, count := b.botComment(b.site, n, "rootsite")
	if count != 1 || c.User == "" || !strings.HasSuffix(c.User, "[bot]") || strings.Contains(c.Body, "<b>") {
		t.Fatalf("comment: %+v (%d)", c, count)
	}
	var sawStatus bool
	for _, st := range b.gh.Statuses() {
		if st.SHA == shaA && st.Context == previewStatusContext && st.State == "success" {
			sawStatus = st.TargetURL == runA.PreviewURL
		}
	}
	if !sawStatus {
		t.Fatalf("statuses: %+v", b.gh.Statuses())
	}
	// bobsite has no build settings: its comment says so, without a link.
	b.eventually("bobsite's comment", func() bool {
		c, _ := b.botComment(b.site, n, "bobsite")
		return strings.Contains(c.Body, "could not be built")
	})
	// The token for the comment could only write statuses and pull
	// requests of the one repository.
	for _, tok := range b.gh.Tokens() {
		if tok.Permissions["pull_requests"] != "" && (len(tok.Permissions) != 2 || tok.Permissions["statuses"] != "write" ||
			len(tok.RepositoryIDs) != 1 || tok.RepositoryIDs[0] != b.site) {
			t.Errorf("token: %+v", tok)
		}
	}

	// A push to the pull request: a new preview replaces the old one, and
	// the same comment is updated.
	shaB := b.gh.SetFiles(b.site, "feature", map[string]string{"index.html": "B"})
	b.gh.PushToPull(b.site, n, shaB)
	b.mustReceive(b.delivery("pull_request", b.pull("synchronize", b.site, n)), DeliveryHandled, 2)
	runB := b.waitRun("rootsite", shaB)
	if runB.Status != RunSucceeded || runB.Release == runA.Release {
		t.Fatalf("second run: %+v", runB)
	}
	if got := b.svc.deploys.Tagged("rootsite", previewTag(b.site, n)); len(got) != 1 || got[0] != runB.Release {
		t.Fatalf("previews kept: %v", got)
	}
	b.eventually("the updated comment", func() bool {
		c, _ := b.botComment(b.site, n, "rootsite")
		return strings.Contains(c.Body, runB.PreviewURL)
	})
	if c, count := b.botComment(b.site, n, "rootsite"); count != 1 || c.Edits == 0 {
		t.Fatalf("comment: %+v (%d)", c, count)
	}
	// The run of the first commit no longer links to a preview.
	for _, r := range b.runs("rootsite") {
		if r.ID == runA.ID && (r.PreviewURL != "" || !r.PreviewGone) {
			t.Errorf("the replaced preview still has a link: %+v", r)
		}
	}

	// Closing removes the preview, never the live version.
	b.gh.ClosePull(b.site, n, true)
	b.mustReceive(b.delivery("pull_request", b.pull("closed", b.site, n)), DeliveryHandled, 0)
	b.svc.runner.bg.Wait()
	if got := b.svc.deploys.Tagged("rootsite", previewTag(b.site, n)); len(got) != 0 {
		t.Fatalf("previews after closing: %v", got)
	}
	if cur := b.svc.deploys.Current("rootsite"); cur != live.Release || !b.svc.deploys.HasRelease("rootsite", live.Release) {
		t.Fatalf("live version: %s", cur)
	}
	if c, _ := b.botComment(b.site, n, "rootsite"); !strings.Contains(c.Body, "was closed") || strings.Contains(c.Body, "ownpanel-preview=") {
		t.Fatalf("comment after closing: %s", c.Body)
	}
	b.assertNoSecrets("log", b.logs.String())
}

func TestPullRequestClosedWhileBuilding(t *testing.T) {
	b := newPreviewWorld(t)
	release := make(chan struct{})
	b.setResult(func(spec build.Spec) buildtest.Result {
		<-release
		return buildtest.Result{Output: buildtest.SiteArchive(map[string]string{"index.html": "x"})}
	})
	sha := b.gh.SetFiles(b.site, "feature", map[string]string{"index.html": "A"})
	n := b.gh.OpenPull(b.site, "main", "feature", 0, sha, "Change", "ann")
	b.mustReceive(b.delivery("pull_request", b.pull("opened", b.site, n)), DeliveryHandled, 2)
	b.waitRun("rootsite", sha, RunRunning)
	b.gh.ClosePull(b.site, n, false)
	b.mustReceive(b.delivery("pull_request", b.pull("closed", b.site, n)), DeliveryHandled, 0)
	close(release)
	r := b.waitRun("rootsite", sha)
	if r.Status != RunCancelled || r.Detail != runPullRequestClosed {
		t.Fatalf("run: %+v", r)
	}
	b.svc.runner.bg.Wait()
	if got := b.svc.deploys.Tagged("rootsite", previewTag(b.site, n)); len(got) != 0 {
		t.Fatalf("a closed pull request got a preview: %v", got)
	}
}

func TestPullRequestRunsDoNotReplaceOtherRuns(t *testing.T) {
	b := newPreviewWorld(t)
	b.stopRunner()
	pushSHA := b.pushCommit(map[string]string{"index.html": "live"})
	sha1 := b.gh.SetFiles(b.site, "one", map[string]string{"index.html": "1"})
	sha2 := b.gh.SetFiles(b.site, "two", map[string]string{"index.html": "2"})
	n1 := b.gh.OpenPull(b.site, "main", "one", 0, sha1, "One", "ann")
	n2 := b.gh.OpenPull(b.site, "main", "two", 0, sha2, "Two", "ann")
	b.mustReceive(b.delivery("pull_request", b.pull("opened", b.site, n1)), DeliveryHandled, 2)
	b.mustReceive(b.delivery("pull_request", b.pull("opened", b.site, n2)), DeliveryHandled, 2)
	queued := 0
	for _, r := range b.runs("rootsite") {
		if r.Status == RunQueued {
			queued++
		}
	}
	if queued != 3 {
		t.Fatalf("runs: %+v", b.runs("rootsite"))
	}
	b.startRunner()
	for _, sha := range []string{pushSHA, sha1, sha2} {
		if r := b.waitRun("rootsite", sha); r.Status != RunSucceeded {
			t.Fatalf("run %s: %+v", sha, r)
		}
	}
	if cur := b.svc.deploys.Current("rootsite"); cur != b.waitRun("rootsite", pushSHA).Release {
		t.Fatalf("live: %s", cur)
	}
}

func TestPullRequestFromFork(t *testing.T) {
	b := newPreviewWorld(t)
	ctx := context.Background()
	fork := b.gh.AddRepo("stranger", "site", false)
	sha := b.gh.SetFiles(fork, "feature", map[string]string{"index.html": "fork"})
	n := b.gh.OpenPull(b.site, "main", "feature", fork, sha, "From a fork", "stranger")

	// Off by default: ignored.
	b.mustReceive(b.delivery("pull_request", b.pull("opened", b.site, n)), DeliveryIgnored, 0)

	if _, err := b.svc.SetSitePreviews(ctx, rootSess, "rootsite", SitePreviewsInput{Forks: true}); KindOf(err) != Invalid {
		t.Fatalf("forks without pull requests: %v", err)
	}
	if _, err := b.svc.SetSitePreviews(ctx, bobSess, "rootsite", SitePreviewsInput{PullRequests: true, Forks: true}); KindOf(err) != NotFound {
		t.Fatalf("someone else's site: %v", err)
	}
	st, err := b.svc.SetSitePreviews(ctx, rootSess, "rootsite", SitePreviewsInput{PullRequests: true, Forks: true})
	if err != nil || !st.Source.PullRequestPreviews || !st.Source.ForkPreviews {
		t.Fatalf("set: %+v %v", st.Source, err)
	}
	b.mustReceive(b.delivery("pull_request", b.pull("reopened", b.site, n)), DeliveryHandled, 1)
	r := b.waitRun("rootsite", sha)
	if r.Status != RunSucceeded || !r.Fork || r.HeadRepository != "stranger/site" {
		t.Fatalf("run: %+v", r)
	}
	// Built without any of the site's variables, with a cache of its own.
	spec := b.specOf("rootsite", sha)
	if len(spec.Env) != 0 || len(spec.Secrets) != 0 || !spec.Untrusted {
		t.Fatalf("fork build got %d variables, %d secrets, untrusted %v", len(spec.Env), len(spec.Secrets), spec.Untrusted)
	}
	b.eventually("the comment", func() bool {
		c, _ := b.botComment(b.site, n, "rootsite")
		return strings.Contains(c.Body, "from a fork")
	})

	// Switched off again: a queued run fails when the runner takes it.
	b.stopRunner()
	sha2 := b.gh.SetFiles(fork, "feature", map[string]string{"index.html": "fork 2"})
	b.gh.PushToPull(b.site, n, sha2)
	b.mustReceive(b.delivery("pull_request", b.pull("synchronize", b.site, n)), DeliveryHandled, 1)
	if _, err := b.svc.SetSitePreviews(ctx, rootSess, "rootsite", SitePreviewsInput{PullRequests: true}); err != nil {
		t.Fatal(err)
	}
	b.startRunner()
	if r := b.waitRun("rootsite", sha2); r.Status != RunFailed || !strings.Contains(r.Detail, "forks are switched off") {
		t.Fatalf("run after switching off: %+v", r)
	}
	acts, _ := b.svc.store.Activities(ctx, 100)
	if !strings.Contains(asJSON(t, acts), "site.previews") {
		t.Errorf("no activity entry")
	}
}

func TestPullRequestPreviewOnPublicRepository(t *testing.T) {
	b := newPreviewWorld(t)
	b.gh.AddBranch(b.other, "main")
	b.link("rootsite", b.other, "main", true)
	sha := b.gh.SetFiles(b.other, "feature", map[string]string{"index.html": "A"})
	n := b.gh.OpenPull(b.other, "main", "feature", 0, sha, "Change", "ann")
	// Someone writes the panel's marker in a comment of their own: it is
	// never taken for the panel's.
	fake := b.gh.AddComment(b.other, n, "mallory", previewMarker("rootsite")+" look here")
	b.mustReceive(b.delivery("pull_request", b.pull("opened", b.other, n)), DeliveryHandled, 1)
	r := b.waitRun("rootsite", sha)
	if r.Status != RunSucceeded || r.PreviewURL == "" {
		t.Fatalf("run: %+v", r)
	}
	b.eventually("the comment", func() bool { _, count := b.botComment(b.other, n, "rootsite"); return count == 1 })
	c, _ := b.botComment(b.other, n, "rootsite")
	if strings.Contains(c.Body, "ownpanel-preview=") || strings.Contains(c.Body, r.PreviewURL) ||
		!strings.Contains(c.Body, "https://panel.example.com:8443/#/sites/rootsite?run=") {
		t.Fatalf("comment on a public repository: %s", c.Body)
	}
	for _, cm := range b.gh.Comments(b.other, n) {
		if cm.ID == fake && cm.Edits != 0 {
			t.Fatal("someone else's comment was changed")
		}
	}
	for _, st := range b.gh.Statuses() {
		if st.Context == previewStatusContext && strings.Contains(st.TargetURL, "ownpanel-preview=") {
			t.Fatalf("status with the key: %+v", st)
		}
	}

	// Made private → public: previews of its pull requests go.
	b.mustReceive(b.delivery("repository", b.gh.RepositoryPayload("publicized", b.other, b.inst)), DeliveryHandled, 0)
	b.svc.runner.bg.Wait()
	if tags := b.svc.deploys.Tags("rootsite"); len(tags) != 0 {
		t.Fatalf("tags left: %v", tags)
	}
}

func TestPullRequestsThatCreateNoRun(t *testing.T) {
	b := newPreviewWorld(t)
	ctx := context.Background()
	b.gh.AddBranch(b.site, "dev")
	sha := b.gh.SetFiles(b.site, "feature", map[string]string{"index.html": "A"})
	other := b.gh.OpenPull(b.site, "dev", "feature", 0, sha, "Into dev", "ann")
	b.mustReceive(b.delivery("pull_request", b.pull("opened", b.site, other)), DeliveryIgnored, 0)
	n := b.gh.OpenPull(b.site, "main", "feature", 0, sha, "Into main", "ann")
	for _, action := range []string{"edited", "labeled", "assigned", "ready_for_review"} {
		b.mustReceive(b.delivery("pull_request", b.pull(action, b.site, n)), DeliveryIgnored, 0)
	}
	for _, site := range []string{"rootsite", "bobsite"} {
		if _, err := b.svc.SetSitePreviews(ctx, rootSess, site, SitePreviewsInput{}); err != nil {
			t.Fatal(err)
		}
	}
	b.mustReceive(b.delivery("pull_request", b.pull("opened", b.site, n)), DeliveryIgnored, 0)
	// Linking anew keeps the setting.
	b.link("rootsite", b.site, "main", true)
	if st, err := b.svc.GetSiteSource(ctx, rootSess, "rootsite"); err != nil || st.Source.PullRequestPreviews {
		t.Fatalf("linking anew switched previews on: %+v %v", st.Source, err)
	}
}
