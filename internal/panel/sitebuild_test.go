package panel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/build/buildtest"
	"github.com/danielgtmn/ownpanel/internal/store"
)

type recordedRuns struct {
	mu      sync.Mutex
	updates []RunUpdate
}

func (r *recordedRuns) Report(_ context.Context, u RunUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, u)
}

func (r *recordedRuns) statuses(runID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, u := range r.updates {
		if u.RunID == runID {
			out = append(out, u.Status)
		}
	}
	return out
}

func (r *recordedRuns) last(runID string) RunUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.updates) - 1; i >= 0; i-- {
		if r.updates[i].RunID == runID {
			return r.updates[i]
		}
	}
	return RunUpdate{}
}

const testSecret = "hunter2-very-secret"

// newBuildService is a service with a site "blog" (owned by admin) and a
// fake build service.
func newBuildService(t *testing.T) (*Service, *buildtest.Fake, *recordedRuns, string) {
	t.Helper()
	svc, _, sites := newService(t)
	fake := buildtest.Start(t)
	runs := &recordedRuns{}
	svc.builds = fake.Client()
	svc.build.runs = runs
	svc.build.poll = 5 * time.Millisecond
	if _, err := svc.SaveSite(context.Background(), admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	return svc, fake, runs, sites
}

func source() *bytes.Reader {
	return bytes.NewReader(buildtest.SiteArchive(map[string]string{"package.json": "{}", "src/main.js": "x"}))
}

var settings = BuildSettings{
	Command: "npm ci\nnpm run build", Output: "dist", Image: "node-24",
	Env: map[string]string{"MODE": "production"}, Secrets: map[string]string{"API_KEY": testSecret},
}

func req(runID string, live bool) BuildRequest {
	return BuildRequest{Site: "blog", Tree: source(), Settings: settings, Live: live, RunID: runID}
}

// noPartialVersion fails when a release or a leftover of one exists.
func noPartialVersion(t *testing.T, svc *Service, sites string) {
	t.Helper()
	if r := svc.deploys.Releases("blog"); len(r) != 0 {
		t.Errorf("releases: %v", r)
	}
	entries, _ := os.ReadDir(filepath.Join(sites, "blog", "releases"))
	for _, e := range entries {
		t.Errorf("left in releases: %s", e.Name())
	}
}

func TestBuildSiteSucceeds(t *testing.T) {
	svc, fake, runs, _ := newBuildService(t)
	ctx := context.Background()

	res, err := svc.BuildSite(ctx, admin, req("run-1", true))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != RunSucceeded || res.Publication == nil || !res.Publication.Live {
		t.Fatalf("result: %+v", res)
	}
	if cur := svc.deploys.Current("blog"); cur != res.Publication.Release {
		t.Errorf("live %q, built %q", cur, res.Publication.Release)
	}
	got, _ := os.ReadFile(filepath.Join(svc.deploys.Root("blog"), "index.html"))
	if string(got) != "built" {
		t.Errorf("index.html: %q", got)
	}
	if st := runs.statuses("run-1"); !slices.Equal(st, []string{RunQueued, RunRunning, RunSucceeded}) {
		t.Errorf("statuses: %v", st)
	}
	last := runs.last("run-1")
	if last.Release != res.Publication.Release || !last.Live || last.QueuedAt.IsZero() || last.StartedAt.IsZero() || last.FinishedAt.IsZero() {
		t.Errorf("last update: %+v", last)
	}
	specs := fake.Specs()
	if len(specs) != 1 || specs[0].Site != "blog" || specs[0].Image != "node-24" || specs[0].Output != "dist" ||
		specs[0].Secrets["API_KEY"] != testSecret || specs[0].Env["MODE"] != "production" {
		t.Fatalf("specs: %v", specs)
	}
	if fake.Known() != 0 || !slices.Equal(fake.Removed(), []uint64{specs[0].ID}) {
		t.Errorf("not removed: known %d, removed %v", fake.Known(), fake.Removed())
	}
	acts, _ := svc.store.Activities(ctx, 50)
	found := false
	for _, a := range acts {
		if strings.Contains(a.Detail, testSecret) || strings.Contains(a.Target, testSecret) {
			t.Errorf("secret in the activity log: %+v", a)
		}
		found = found || (a.Action == "site.build-publish" && a.Detail == res.Publication.Release)
	}
	if !found {
		t.Errorf("no activity entry: %+v", acts)
	}

	// Not live: a preview, the live version stays.
	before := svc.deploys.Current("blog")
	res, err = svc.BuildSite(ctx, admin, req("run-2", false))
	if err != nil || res.Publication == nil || res.Publication.Live {
		t.Fatalf("not live: %+v %v", res, err)
	}
	if svc.deploys.Current("blog") != before || !svc.deploys.Pending("blog", res.Publication.Release) {
		t.Errorf("the live version changed or the new one is not pending")
	}
}

func TestBuildSiteFails(t *testing.T) {
	svc, fake, runs, sites := newBuildService(t)
	fake.Result = func(build.Spec) buildtest.Result {
		return buildtest.Result{State: build.StateFailed, Reason: build.ReasonExit, ExitCode: 1,
			Message: "the build command failed with exit code 1",
			Log:     "npm ERR! token " + testSecret + " rejected\nnpm ERR! failed\n"}
	}
	res, err := svc.BuildSite(context.Background(), admin, req("r", true))
	if KindOf(err) != Invalid || CodeOf(err) != CodeBuildFailed {
		t.Fatalf("error: %v", err)
	}
	if res.Status != RunFailed || res.Reason != build.ReasonExit || res.ExitCode != 1 || res.Publication != nil {
		t.Errorf("result: %+v", res)
	}
	if strings.Contains(res.LogTail, testSecret) || !strings.Contains(res.LogTail, "npm ERR! token *** rejected") {
		t.Errorf("log tail: %q", res.LogTail)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Errorf("secret in the error: %v", err)
	}
	if last := runs.last("r"); last.Status != RunFailed || last.Reason != build.ReasonExit || last.ExitCode != 1 {
		t.Errorf("reported: %+v", last)
	}
	noPartialVersion(t, svc, sites)
	if fake.Known() != 0 {
		t.Error("the build was not removed")
	}

	// A secret too short to be blanked out keeps the log out of the answer.
	short := req("s", true)
	short.Settings.Secrets = map[string]string{"PIN": "42"}
	res, _ = svc.BuildSite(context.Background(), admin, short)
	if res.LogTail != "" {
		t.Errorf("log with a short secret: %q", res.LogTail)
	}
}

func TestBuildSiteTimeout(t *testing.T) {
	svc, fake, runs, sites := newBuildService(t)
	hold := make(chan struct{})
	defer close(hold)
	fake.Result = func(build.Spec) buildtest.Result { return buildtest.Result{Hold: hold} }
	svc.build.timeout = 100 * time.Millisecond
	res, err := svc.BuildSite(context.Background(), admin, req("r", true))
	if CodeOf(err) != CodeBuildFailed || res.Status != RunFailed || res.Reason != build.ReasonTimeout {
		t.Fatalf("%+v %v", res, err)
	}
	if len(fake.Cancelled()) != 1 || fake.Known() != 0 {
		t.Errorf("cancelled %v, known %d", fake.Cancelled(), fake.Known())
	}
	if last := runs.last("r"); last.Status != RunFailed || last.Reason != build.ReasonTimeout {
		t.Errorf("reported: %+v", last)
	}
	noPartialVersion(t, svc, sites)
}

func TestBuildSiteCancelled(t *testing.T) {
	svc, fake, runs, sites := newBuildService(t)
	hold := make(chan struct{})
	defer close(hold)
	fake.Result = func(build.Spec) buildtest.Result { return buildtest.Result{Hold: hold} }
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for !fake.Running("blog") {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	res, err := svc.BuildSite(ctx, admin, req("r", true))
	if KindOf(err) != Conflict || res.Status != RunCancelled {
		t.Fatalf("%+v %v", res, err)
	}
	if len(fake.Cancelled()) != 1 || fake.Known() != 0 {
		t.Errorf("cancelled %v, known %d", fake.Cancelled(), fake.Known())
	}
	if st := runs.statuses("r"); st[len(st)-1] != RunCancelled {
		t.Errorf("statuses: %v", st)
	}
	noPartialVersion(t, svc, sites)

	// The service cancels it (e.g. an administrator stopped it there).
	fake.Result = func(build.Spec) buildtest.Result { return buildtest.Result{State: build.StateCancelled} }
	if res, _ := svc.BuildSite(context.Background(), admin, req("r2", true)); res.Status != RunCancelled {
		t.Errorf("cancelled by the service: %+v", res)
	}
	noPartialVersion(t, svc, sites)
}

func TestBuildSiteUntrustedOutput(t *testing.T) {
	svc, fake, _, sites := newBuildService(t)

	// A link in the output.
	fake.Result = func(build.Spec) buildtest.Result { return buildtest.Result{Output: buildtest.SymlinkArchive()} }
	res, err := svc.BuildSite(context.Background(), admin, req("r", true))
	if CodeOf(err) != CodeBuildFailed || res.Reason != build.ReasonOutput || !strings.Contains(res.Message, "link") {
		t.Fatalf("link: %+v %v", res, err)
	}
	noPartialVersion(t, svc, sites)

	// Output larger than the stream limit.
	defer func(old int64) { maxOutputStream = old }(maxOutputStream)
	maxOutputStream = 1 << 10
	big := make([]byte, 4<<10)
	for i := range big {
		big[i] = byte(i*7919 + i>>3)
	}
	fake.Result = func(build.Spec) buildtest.Result {
		return buildtest.Result{Output: buildtest.SiteArchive(map[string]string{"index.html": string(big)})}
	}
	res, err = svc.BuildSite(context.Background(), admin, req("r", true))
	if CodeOf(err) != CodeBuildFailed || res.Reason != build.ReasonOutput || !strings.Contains(res.Message, "larger") {
		t.Fatalf("too large: %+v %v", res, err)
	}
	noPartialVersion(t, svc, sites)

	// Not an archive at all.
	maxOutputStream = 1 << 20
	fake.Result = func(build.Spec) buildtest.Result { return buildtest.Result{Output: []byte("<html>")} }
	if res, err := svc.BuildSite(context.Background(), admin, req("r", true)); CodeOf(err) != CodeBuildFailed || res.Reason != build.ReasonOutput {
		t.Fatalf("not an archive: %+v %v", res, err)
	}
	noPartialVersion(t, svc, sites)
	if fake.Known() != 0 {
		t.Error("builds left in the service")
	}
}

func TestBuildSiteNewerReplacesWaiting(t *testing.T) {
	svc, fake, runs, _ := newBuildService(t)
	hold := make(chan struct{})
	fake.Result = func(spec build.Spec) buildtest.Result {
		if spec.Env["N"] == "1" {
			return buildtest.Result{Hold: hold, Output: buildtest.SiteArchive(map[string]string{"index.html": "1"})}
		}
		return buildtest.Result{Output: buildtest.SiteArchive(map[string]string{"index.html": spec.Env["N"]})}
	}
	start := func(n string) <-chan BuildResult {
		ch := make(chan BuildResult, 1)
		r := req("run-"+n, true)
		r.Settings.Env = map[string]string{"N": n}
		go func() {
			res, err := svc.BuildSite(context.Background(), admin, r)
			if err != nil {
				res.Message = res.Message + " / " + err.Error()
			}
			ch <- res
		}()
		return ch
	}
	waiting := func() bool {
		svc.build.line.mu.Lock()
		defer svc.build.line.mu.Unlock()
		sl := svc.build.line.sites["blog"]
		return sl != nil && sl.waiting != nil
	}
	first := start("1")
	for !fake.Running("blog") {
		time.Sleep(time.Millisecond)
	}
	second := start("2")
	for !waiting() {
		time.Sleep(time.Millisecond)
	}
	third := start("3")
	res := <-second
	if res.Status != RunSkipped {
		t.Fatalf("second: %+v", res)
	}
	if st := runs.statuses("run-2"); !slices.Equal(st, []string{RunQueued, RunSkipped}) {
		t.Errorf("second statuses: %v", st)
	}
	close(hold)
	if res := <-first; res.Status != RunSucceeded {
		t.Errorf("first: %+v", res)
	}
	if res := <-third; res.Status != RunSucceeded {
		t.Errorf("third: %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(svc.deploys.Root("blog"), "index.html"))
	if string(got) != "3" {
		t.Errorf("live: %q", got)
	}
	// The second build never started and was removed.
	if len(fake.Started()) != 2 || fake.Known() != 0 {
		t.Errorf("started %v, known %d", fake.Started(), fake.Known())
	}
	if svc.build.line.siteBuilding("blog") {
		t.Error("the line of the site is not empty")
	}
}

func TestBuildLine(t *testing.T) {
	l := buildLine{sites: map[string]*siteLine{}}
	ctx := context.Background()
	a := l.enter("s")
	if err := l.wait(ctx, a); err != nil {
		t.Fatal(err)
	}
	other := l.enter("other") // other sites do not wait
	if err := l.wait(ctx, other); err != nil {
		t.Fatal(err)
	}
	b := l.enter("s")
	c := l.enter("s")
	if err := l.wait(ctx, b); !errors.Is(err, errReplaced) {
		t.Errorf("b: %v", err)
	}
	l.leave("s", b) // a replaced ticket leaving changes nothing
	cctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := l.wait(cctx, c); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("c before a left: %v", err)
	}
	l.leave("s", a)
	if err := l.wait(ctx, c); err != nil {
		t.Errorf("c: %v", err)
	}
	// One that gives up waiting leaves the line.
	d := l.enter("s")
	l.leave("s", d)
	l.leave("s", c)
	l.leave("other", other)
	if len(l.sites) != 0 {
		t.Errorf("left: %v", l.sites)
	}
}

func TestBuildSitePermissions(t *testing.T) {
	svc, fake, _, _ := newBuildService(t)
	ctx := context.Background()
	bob := addUser(t, svc, "bob", store.RoleMember)
	member := Actor{UserID: bob.ID, Name: "bob", Via: ViaSession}
	if _, err := svc.BuildSite(ctx, member, req("", true)); KindOf(err) != NotFound {
		t.Errorf("someone else's site: %v", err)
	}
	if _, err := svc.BuildSite(ctx, Actor{}, req("", true)); KindOf(err) != Unauthenticated {
		t.Errorf("nobody: %v", err)
	}
	readOnly := admin
	readOnly.ReadOnly, readOnly.Via = true, ViaToken
	if _, err := svc.BuildSite(ctx, readOnly, req("", true)); KindOf(err) != Forbidden {
		t.Errorf("read-only token: %v", err)
	}
	limited := admin
	limited.Sites, limited.Via = []string{"other"}, ViaToken
	if _, err := svc.BuildSite(ctx, limited, req("", true)); KindOf(err) != NotFound {
		t.Errorf("token for other sites: %v", err)
	}
	// Builds from an upload are for administrators.
	if _, err := svc.SaveSite(ctx, member, "bobs", nil); err != nil {
		t.Fatal(err)
	}
	r := req("", true)
	if _, err := svc.BuildSiteFromUpload(ctx, member, "bobs", r.Tree, settings, true); KindOf(err) != Forbidden {
		t.Errorf("member upload: %v", err)
	}
	if len(fake.Specs()) != 0 {
		t.Errorf("a refused build reached the service: %v", fake.Specs())
	}
	// The owner may build the site (the permission of publishing).
	r = req("", true)
	r.Site = "bobs"
	if res, err := svc.BuildSite(ctx, member, r); err != nil || res.Status != RunSucceeded {
		t.Errorf("owner: %+v %v", res, err)
	}
	if res, err := svc.BuildSiteFromUpload(ctx, admin, "blog", source(), settings, false); err != nil || res.Status != RunSucceeded {
		t.Errorf("admin upload: %+v %v", res, err)
	}
}

func TestBuildSiteRefusals(t *testing.T) {
	svc, fake, _, _ := newBuildService(t)
	ctx := context.Background()

	bad := req("", true)
	bad.Settings.Image = "alpine"
	if _, err := svc.BuildSite(ctx, admin, bad); KindOf(err) != Invalid {
		t.Errorf("image: %v", err)
	}
	bad = req("", true)
	bad.Settings.Output = "../etc"
	if _, err := svc.BuildSite(ctx, admin, bad); KindOf(err) != Invalid {
		t.Errorf("output: %v", err)
	}
	bad = req("", true)
	bad.Settings.Secrets = map[string]string{"LD_PRELOAD": testSecret}
	if _, err := svc.BuildSite(ctx, admin, bad); KindOf(err) != Invalid || strings.Contains(err.Error(), testSecret) {
		t.Errorf("reserved variable: %v", err)
	}
	if len(fake.Specs()) != 0 {
		t.Errorf("refused builds reached the service")
	}

	// The service is busy.
	fake.Busy = true
	res, err := svc.BuildSite(ctx, admin, req("", true))
	if KindOf(err) != Throttled || res.Status != RunFailed {
		t.Errorf("busy: %+v %v", res, err)
	}
	fake.Busy = false

	// The source is refused.
	bad = req("", true)
	bad.Tree = strings.NewReader("not an archive")
	res, err = svc.BuildSite(ctx, admin, bad)
	if KindOf(err) != Invalid || res.Reason != build.ReasonSource || fake.Known() != 0 {
		t.Errorf("source: %+v %v (known %d)", res, err, fake.Known())
	}

	// No build service.
	svc.builds = nil
	if _, err := svc.BuildSite(ctx, admin, req("", true)); KindOf(err) != Unavailable {
		t.Errorf("not configured: %v", err)
	}

	// The panel's own fetcher: the site has no repository, and the reason
	// is part of the message.
	svc.builds = fake.Client()
	if _, err := svc.BuildCommit(ctx, admin, SourceRef{Site: "blog", Commit: "abc"}, settings, true, ""); KindOf(err) != Unavailable ||
		!strings.Contains(err.Error(), "not linked to a repository") {
		t.Errorf("no repository: %v", err)
	}
	// No source fetcher.
	svc.build.sources = nil
	if _, err := svc.BuildCommit(ctx, admin, SourceRef{Site: "blog", Commit: "abc"}, settings, true, ""); KindOf(err) != Unavailable {
		t.Errorf("no fetcher: %v", err)
	}
}

type fakeSources struct{ got *SourceRef }

func (f fakeSources) FetchSource(_ context.Context, ref SourceRef) (io.ReadCloser, int, error) {
	*f.got = ref
	return io.NopCloser(source()), 1, nil
}

func TestBuildCommit(t *testing.T) {
	svc, _, runs, _ := newBuildService(t)
	var got SourceRef
	svc.build.sources = fakeSources{&got}
	res, err := svc.BuildCommit(context.Background(), admin, SourceRef{Site: "blog", Commit: "abc123"}, settings, true, "run-9")
	if err != nil || res.Status != RunSucceeded || got.Commit != "abc123" || got.Site != "blog" {
		t.Fatalf("%+v %v %+v", res, err, got)
	}
	if st := runs.statuses("run-9"); len(st) != 3 {
		t.Errorf("statuses: %v", st)
	}
	bob := addUser(t, svc, "bob", store.RoleMember)
	if _, err := svc.BuildCommit(context.Background(), Actor{UserID: bob.ID, Via: ViaSession}, SourceRef{Site: "blog"}, settings, true, ""); KindOf(err) != NotFound {
		t.Errorf("someone else's site: %v", err)
	}
}

func TestRemoveStaleBuilds(t *testing.T) {
	svc, fake, _, _ := newBuildService(t)
	fake.Add(svc.build.idBase-5, "blog", build.StateRunning)
	fake.Add(svc.build.idBase, "blog", build.StateSucceeded)
	fake.Add(svc.build.idBase+1, "blog", build.StateQueued) // this run's
	svc.RemoveStaleBuilds(context.Background())
	if got := fake.Removed(); len(got) != 2 || fake.Known() != 1 {
		t.Errorf("removed %v, known %d", got, fake.Known())
	}
	if id := svc.build.newID(); id <= svc.build.idBase {
		t.Errorf("new id %d", id)
	}
}
