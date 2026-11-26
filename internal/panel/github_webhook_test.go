package panel

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// hookWorld is a GitHub world with acme connected and rootsite and bobsite
// linked to acme/site, branch main, publishing on push.
type hookWorld struct {
	*githubWorld
	app  GitHubApp
	inst int64
	n    int
}

func newHookWorld(t *testing.T) *hookWorld {
	t.Helper()
	g, app, inst := linkedWorld(t)
	h := &hookWorld{githubWorld: g, app: app, inst: inst}
	for _, site := range []string{"rootsite", "bobsite"} {
		h.link(site, g.site, "main", true)
	}
	return h
}

func (h *hookWorld) link(site string, repo int64, branch string, push bool) {
	h.t.Helper()
	if _, err := h.svc.SetSiteSource(context.Background(), rootSess, site, SiteSourceInput{
		InstallationID: h.inst, RepositoryID: repo, Branch: branch, PublishOnPush: push,
	}); err != nil {
		h.t.Fatal(err)
	}
}

// delivery is a delivery of the connected App, signed as GitHub signs it.
func (h *hookWorld) delivery(event string, body []byte) GitHubDelivery {
	h.n++
	return h.deliveryWithID(event, fmt.Sprintf("72d3162e-cc78-11e3-81ab-%012d", h.n), body)
}

func (h *hookWorld) deliveryWithID(event, id string, body []byte) GitHubDelivery {
	hd := h.gh.Headers(h.app.AppID, event, id, body)
	return GitHubDelivery{
		ID: hd.Get(github.HeaderDelivery), Event: hd.Get(github.HeaderEvent), TargetType: hd.Get(github.HeaderTargetType),
		TargetID: hd.Get(github.HeaderTargetID), Signature: hd.Get(github.HeaderSignature), Body: body,
	}
}

func (h *hookWorld) receive(d GitHubDelivery) (GitHubDeliveryResult, error) {
	return h.svc.ReceiveGitHubDelivery(context.Background(), d)
}

func (h *hookWorld) mustReceive(d GitHubDelivery, want string, runs int) {
	h.t.Helper()
	res, err := h.receive(d)
	if err != nil || res.Result != want || res.Runs != runs {
		h.t.Fatalf("%s delivery: %+v %v, want %s with %d runs", d.Event, res, err, want, runs)
	}
}

func (h *hookWorld) push(p githubtest.Push) []byte {
	if p.RepoID == 0 {
		p.RepoID = h.site
	}
	if p.InstallationID == 0 {
		p.InstallationID = h.inst
	}
	if p.Ref == "" {
		p.Ref = "refs/heads/main"
	}
	if p.Pusher == "" {
		p.Pusher = "ann"
	}
	return h.gh.PushPayload(p)
}

func (h *hookWorld) runs(site string) []Run {
	h.t.Helper()
	list, err := h.svc.ListSiteRuns(context.Background(), rootSess, site)
	if err != nil {
		h.t.Fatal(err)
	}
	return list.Runs
}

const commitA = "6113728f27ae82c7b1a177c8d03f9e96e0adf246"

func TestDeliveryPushCreatesRuns(t *testing.T) {
	h := newHookWorld(t)
	body := h.push(githubtest.Push{After: commitA, Message: "Fix the header\n\nLonger text", AuthorName: "Ann Author", AuthorLogin: "ann"})
	d := h.delivery("push", body)
	h.mustReceive(d, DeliveryHandled, 2)

	for _, site := range []string{"rootsite", "bobsite"} {
		runs := h.runs(site)
		if len(runs) != 1 {
			t.Fatalf("%s: %+v", site, runs)
		}
		r := runs[0]
		if r.Site != site || r.Status != store.RunQueued || r.Trigger != RunTriggerPush || r.Commit != commitA ||
			r.ShortCommit != "6113728" || r.Ref != "refs/heads/main" || r.Branch != "main" || r.Repository != "acme/site" ||
			r.Message != "Fix the header" || r.AuthorName != "Ann Author" || r.AuthorLogin != "ann" || r.Pusher != "ann" ||
			r.Waiting == "" || r.CreatedAt.IsZero() || r.StartedAt != nil {
			t.Fatalf("%s run: %+v", site, r)
		}
	}
	// The site's source shows its newest run.
	st, err := h.svc.GetSiteSource(context.Background(), rootSess, "rootsite")
	if err != nil || st.LastRun == nil || st.LastRun.Commit != commitA {
		t.Fatalf("last run: %+v %v", st.LastRun, err)
	}

	// The same delivery again (a replay, or a redelivery of one already
	// accepted) is ignored.
	h.mustReceive(d, DeliveryDuplicate, 0)
	if runs := h.runs("rootsite"); len(runs) != 1 {
		t.Fatalf("replay created a run: %+v", runs)
	}

	// A newer push replaces the queued run.
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{Message: "Second"})), DeliveryHandled, 2)
	runs := h.runs("rootsite")
	if len(runs) != 2 || runs[0].Message != "Second" || runs[0].Status != store.RunQueued ||
		runs[1].Status != store.RunSkipped || runs[1].Detail != runSupersededDetail || runs[1].Waiting != "" {
		t.Fatalf("superseded: %+v", runs)
	}

	// Nothing of the deliveries' content or secrets is logged.
	h.assertNoSecrets("log", h.logs.String())
	if strings.Contains(h.logs.String(), "Fix the header") {
		t.Fatalf("the log contains a commit message:\n%s", h.logs)
	}
	acts, _ := h.svc.store.Activities(context.Background(), 50)
	for _, a := range acts {
		h.assertNoSecrets("activity", a.Detail)
	}
}

func TestDeliveryPushesThatCreateNoRun(t *testing.T) {
	h := newHookWorld(t)
	h.gh.AddBranch(h.site, "dev")
	h.gh.AddBranch(h.other, "main")
	h.link("rootsite", h.site, "dev", false) // publish on push off
	h.link("bobsite", h.site, "main", true)
	old := h.now.Add(-8 * 24 * time.Hour)
	for name, body := range map[string][]byte{
		"tag":                 h.push(githubtest.Push{Ref: "refs/tags/v1.0"}),
		"branch deleted":      h.push(githubtest.Push{Deleted: true}),
		"other branch":        h.push(githubtest.Push{Ref: "refs/heads/feature"}),
		"publish on push off": h.push(githubtest.Push{Ref: "refs/heads/dev"}),
		"other repository":    h.push(githubtest.Push{RepoID: h.other}),
		"not a branch":        h.push(githubtest.Push{Ref: "refs/pull/1/head"}),
		"older than a week":   h.push(githubtest.Push{PushedAt: old}),
		"unknown install":     h.push(githubtest.Push{InstallationID: 424242}),
	} {
		res, err := h.receive(h.delivery("push", body))
		if err != nil || res.Result != DeliveryIgnored || res.Runs != 0 {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	for _, site := range []string{"rootsite", "bobsite"} {
		if runs := h.runs(site); len(runs) != 0 {
			t.Fatalf("%s has runs: %+v", site, runs)
		}
	}
	// Other events are accepted and ignored.
	h.mustReceive(h.delivery("ping", []byte(`{"zen":"Keep it logically awesome.","hook_id":1}`)), DeliveryIgnored, 0)
	h.mustReceive(h.delivery("pull_request", []byte(`{"action":"opened","number":1,"pull_request":{}}`)), DeliveryIgnored, 0)
}

func TestDeliveryRejected(t *testing.T) {
	h := newHookWorld(t)
	body := h.push(githubtest.Push{After: commitA})
	valid := h.delivery("push", body)

	bad := map[string]GitHubDelivery{}
	d := valid
	d.Signature = ""
	bad["missing signature"] = d
	d = valid
	d.Signature = github.Sign([]byte("not the secret"), body)
	bad["wrong signature"] = d
	d = valid
	d.Body = append([]byte(nil), body...)
	d.Body[len(d.Body)-2] = ' '
	bad["changed body"] = d
	d = valid
	d.TargetID = "999999"
	bad["unknown app"] = d
	d = valid
	d.TargetID = ""
	bad["no app"] = d
	d = valid
	d.TargetType = "repository"
	bad["not an app's hook"] = d
	d = valid
	d.ID = "../../etc"
	bad["odd delivery id"] = d
	d = valid
	d.Event = "Push!"
	bad["odd event"] = d
	for name, body := range map[string]string{
		"malformed JSON":  `{"ref": "refs/heads/main",`,
		"two values":      `{"ref":"refs/heads/main"} {}`,
		"wrong type":      `{"ref": 1}`,
		"not an object":   `[]`,
		"invalid commit":  strings.Replace(string(body), commitA, "6113728F27AE82C7B1A177C8D03F9E96E0ADF246", -1),
		"no repository":   `{"ref":"refs/heads/main","after":"` + commitA + `","installation":{"id":1}}`,
		"invalid UTF-8":   "{\"ref\":\"refs/heads/main\xff\"}",
		"no installation": strings.Replace(string(body), `"installation"`, `"x_installation"`, 1),
	} {
		bad[name] = h.delivery("push", []byte(body))
	}
	for name, d := range bad {
		res, err := h.receive(d)
		if err != ErrDeliveryRejected || res != (GitHubDeliveryResult{}) {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	if runs := h.runs("rootsite"); len(runs) != 0 {
		t.Fatalf("a refused delivery created a run: %+v", runs)
	}
	// A refused delivery is not remembered: the valid one with the same id
	// as a malformed one is handled.
	malformed := bad["malformed JSON"]
	fixed := h.deliveryWithID("push", malformed.ID, body)
	h.mustReceive(fixed, DeliveryHandled, 2)
	h.assertNoSecrets("log", h.logs.String())
}

func TestDeliveryRateLimitPerApp(t *testing.T) {
	h := newHookWorld(t)
	ping := []byte(`{"zen":"x"}`)
	for i := 0; i < int(githubHookAppBurst); i++ {
		h.mustReceive(h.delivery("ping", ping), DeliveryIgnored, 0)
	}
	_, err := h.receive(h.delivery("ping", ping))
	wantKind(t, err, Throttled)
	// Half a second later there is room for one more. Deliveries with a
	// wrong signature are refused as such and take none of it.
	h.now = h.now.Add(time.Second / githubHookAppRate)
	forged := h.delivery("ping", ping)
	forged.Signature = github.Sign([]byte("guess"), ping)
	if _, err := h.receive(forged); err != ErrDeliveryRejected {
		t.Fatalf("forged: %v", err)
	}
	h.mustReceive(h.delivery("ping", ping), DeliveryIgnored, 0)
	_, err = h.receive(h.delivery("ping", ping))
	wantKind(t, err, Throttled)
}

func TestDeliveryOverallLimit(t *testing.T) {
	h := newHookWorld(t)
	for i := 0; i < int(githubHookAllBurst); i++ {
		if !h.svc.AllowGitHubDelivery() {
			t.Fatalf("refused after %d", i)
		}
	}
	if h.svc.AllowGitHubDelivery() {
		t.Fatal("no overall limit")
	}
	h.now = h.now.Add(time.Second)
	if !h.svc.AllowGitHubDelivery() {
		t.Fatal("the limit does not refill")
	}
}

func TestDeliveryInstallationAndRepositoryChanges(t *testing.T) {
	h := newHookWorld(t)
	ctx := context.Background()
	status := func(site string) string {
		t.Helper()
		st, err := h.svc.GetSiteSource(ctx, rootSess, site)
		if err != nil || st.Source == nil {
			t.Fatalf("source: %+v %v", st, err)
		}
		return st.Source.Status + " " + st.Source.Repository
	}

	// Renamed on GitHub: the new name is shown at once.
	h.gh.RenameRepo(h.site, "website")
	h.mustReceive(h.delivery("repository", h.gh.RepositoryPayload("renamed", h.site, h.inst)), DeliveryHandled, 0)
	src, _ := h.svc.store.SiteSource(ctx, "rootsite")
	if src.Repository != "acme/website" {
		t.Fatalf("renamed: %+v", src)
	}

	// Removed from the installation: the site says so, a push creates no
	// run; added back, all is well again.
	h.gh.SetInstallationRepos(h.inst, []int64{h.other})
	h.mustReceive(h.delivery("installation_repositories", h.gh.InstallationReposPayload("removed", h.inst, []int64{h.site})), DeliveryHandled, 0)
	if got := status("rootsite"); got != SourceRepositoryMissing+" acme/website" {
		t.Fatalf("removed: %s", got)
	}
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{})), DeliveryIgnored, 0)
	h.gh.SetInstallationRepos(h.inst, nil)
	h.mustReceive(h.delivery("installation_repositories", h.gh.InstallationReposPayload("added", h.inst, []int64{h.site})), DeliveryHandled, 0)
	if got := status("rootsite"); got != SourceLinked+" acme/website" {
		t.Fatalf("added back: %s", got)
	}
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{})), DeliveryHandled, 2)

	// Deleted on GitHub.
	h.mustReceive(h.delivery("repository", h.gh.RepositoryPayload("deleted", h.site, h.inst)), DeliveryHandled, 0)
	if gone, _ := h.svc.store.RepositoryGone(ctx, h.inst, h.site); !gone {
		t.Fatal("deleted: not marked")
	}
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{})), DeliveryIgnored, 0)
	h.link("rootsite", h.site, "main", true) // linking again clears it
	if gone, _ := h.svc.store.RepositoryGone(ctx, h.inst, h.site); gone {
		t.Fatal("linked again: still marked")
	}

	// Suspended and back.
	h.mustReceive(h.delivery("installation", h.gh.InstallationPayload("suspend", h.inst)), DeliveryHandled, 0)
	if got := status("rootsite"); got != SourceSuspended+" acme/website" {
		t.Fatalf("suspended: %s", got)
	}
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{})), DeliveryIgnored, 0)
	h.mustReceive(h.delivery("installation", h.gh.InstallationPayload("unsuspend", h.inst)), DeliveryHandled, 0)
	if got := status("rootsite"); got != SourceLinked+" acme/website" {
		t.Fatalf("unsuspended: %s", got)
	}

	// Uninstalled: the sites keep their link and say the connection is
	// missing; pushes create no runs.
	h.mustReceive(h.delivery("installation", h.gh.InstallationPayload("deleted", h.inst)), DeliveryHandled, 0)
	h.gh.RemoveInstallation(h.inst)
	for _, site := range []string{"rootsite", "bobsite"} {
		if got := status(site); got != SourceConnectionMissing+" acme/website" {
			t.Fatalf("%s uninstalled: %s", site, got)
		}
	}
	before := len(h.runs("rootsite"))
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{})), DeliveryIgnored, 0)
	if len(h.runs("rootsite")) != before {
		t.Fatal("a push created a run without a connection")
	}
}

func TestDeliveryForAnotherAppsInstallation(t *testing.T) {
	h := newHookWorld(t)
	// A second connection, for another organisation.
	other := h.connect(rootSess, "beta")
	if other.AppID == h.app.AppID {
		t.Fatal("same app")
	}
	// The other App signs a push that names acme's installation: ignored.
	body := h.push(githubtest.Push{})
	hd := h.gh.Headers(other.AppID, "push", "other-app-delivery", body)
	res, err := h.receive(GitHubDelivery{ID: hd.Get(github.HeaderDelivery), Event: "push", TargetType: github.TargetTypeApp,
		TargetID: hd.Get(github.HeaderTargetID), Signature: hd.Get(github.HeaderSignature), Body: body})
	if err != nil || res.Result != DeliveryIgnored {
		t.Fatalf("other app: %+v %v", res, err)
	}
	// And it cannot uninstall acme's installation either.
	body = h.gh.InstallationPayload("deleted", h.inst)
	hd = h.gh.Headers(other.AppID, "installation", "other-app-delivery-2", body)
	res, err = h.receive(GitHubDelivery{ID: hd.Get(github.HeaderDelivery), Event: "installation", TargetType: github.TargetTypeApp,
		TargetID: hd.Get(github.HeaderTargetID), Signature: hd.Get(github.HeaderSignature), Body: body})
	if err != nil || res.Result != DeliveryIgnored {
		t.Fatalf("other app uninstalls: %+v %v", res, err)
	}
	if _, err := h.svc.store.GitHubInstallation(context.Background(), h.inst); err != nil {
		t.Fatal(err)
	}
}

func TestRunsVisibilityAndText(t *testing.T) {
	h := newHookWorld(t)
	ctx := context.Background()
	msg := "  <script>alert(1)</script>‮ evil\x07 " + strings.Repeat("x", 300) + "\nsecond line"
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{Message: msg, AuthorName: "Eve​\x1b[31m", AuthorLogin: "not a login!"})), DeliveryHandled, 2)
	r := h.runs("rootsite")[0]
	if !strings.HasPrefix(r.Message, "<script>alert(1)</script> evil ") || strings.ContainsAny(r.Message, "‮\x07\n") ||
		len([]rune(r.Message)) != runMessageMax || !strings.HasSuffix(r.Message, "…") {
		t.Fatalf("message %q", r.Message)
	}
	if r.AuthorName != "Eve[31m" || r.AuthorLogin != "" {
		t.Fatalf("author %q %q", r.AuthorName, r.AuthorLogin)
	}

	// Runs are seen by whoever sees the site.
	if list, err := h.svc.ListSiteRuns(ctx, bob, "bobsite"); err != nil || len(list.Runs) != 1 || list.Kept != RunsKept || list.PublishingAvailable {
		t.Fatalf("bob: %+v %v", list, err)
	}
	_, err := h.svc.ListSiteRuns(ctx, bob, "rootsite")
	wantKind(t, err, NotFound)
	_, err = h.svc.ListSiteRuns(ctx, nobody, "bobsite")
	wantKind(t, err, Unauthenticated)
	if st, err := h.svc.GetSiteSource(ctx, bob, "bobsite"); err != nil || st.LastRun == nil {
		t.Fatalf("bob's last run: %+v %v", st, err)
	}
}

func TestRunBuilderContract(t *testing.T) {
	h := newHookWorld(t)
	ctx := context.Background()
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{After: commitA})), DeliveryHandled, 2)
	sites, err := h.svc.SitesWithQueuedRuns(ctx)
	if err != nil || strings.Join(sites, ",") != "bobsite,rootsite" {
		t.Fatalf("queued: %v %v", sites, err)
	}
	r, err := h.svc.ClaimNextRun(ctx, "rootsite")
	if err != nil || r.Status != store.RunRunning || r.Commit != commitA || r.Waiting != "" {
		t.Fatalf("claim: %+v %v", r, err)
	}
	_, err = h.svc.ClaimNextRun(ctx, "rootsite")
	wantKind(t, err, Conflict)
	// A push while it runs waits behind it.
	h.mustReceive(h.delivery("push", h.push(githubtest.Push{})), DeliveryHandled, 2)
	if runs := h.runs("rootsite"); runs[1].Status != store.RunRunning || runs[0].Status != store.RunQueued {
		t.Fatalf("while running: %+v", runs)
	}
	_, err = h.svc.FinishRun(ctx, r.ID, store.RunSkipped, "")
	wantKind(t, err, Invalid)
	if f, err := h.svc.FinishRun(ctx, r.ID, store.RunFailed, "the build failed\nwith details"); err != nil || f.Detail != "the build failed" || f.FinishedAt == nil {
		t.Fatalf("finish: %+v %v", f, err)
	}
	_, err = h.svc.FinishRun(ctx, r.ID, store.RunSucceeded, "")
	wantKind(t, err, Conflict)
	if next, err := h.svc.ClaimNextRun(ctx, "rootsite"); err != nil || next.ID == r.ID {
		t.Fatalf("next: %+v %v", next, err)
	}
}
