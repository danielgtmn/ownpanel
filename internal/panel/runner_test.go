package panel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/build/buildtest"
	"github.com/danielgtmn/ownpanel/internal/github/githubtest"
	"github.com/danielgtmn/ownpanel/internal/store"
)

const buildSecret = "sk_live_very-secret-value"

// buildWorld is a hook world (rootsite and bobsite linked to acme/site,
// main, publishing on push) with a fake build service and the runner
// running.
type buildWorld struct {
	*hookWorld
	fake *buildtest.Fake
	// result decides how builds end (nil: success).
	mu     sync.Mutex
	result func(spec build.Spec) buildtest.Result
	stop   context.CancelFunc
	done   chan struct{}
}

func newBuildWorld(t *testing.T) *buildWorld {
	t.Helper()
	h := newHookWorld(t)
	b := &buildWorld{hookWorld: h, fake: buildtest.Start(t)}
	b.fake.Result = func(spec build.Spec) buildtest.Result {
		b.mu.Lock()
		f := b.result
		b.mu.Unlock()
		if f != nil {
			return f(spec)
		}
		return buildtest.Result{Output: buildtest.SiteArchive(map[string]string{"index.html": "built " + spec.Env["MODE"]}),
			Log: "installing\nusing " + spec.Secrets["API_KEY"] + "\ndone\n"}
	}
	svc := h.svc
	svc.builds = b.fake.Client()
	svc.build.poll = 5 * time.Millisecond
	svc.runner.poll = 20 * time.Millisecond
	t.Cleanup(b.stopRunner)
	b.startRunner()
	return b
}

func (b *buildWorld) startRunner() {
	ctx, cancel := context.WithCancel(context.Background())
	b.stop, b.done = cancel, make(chan struct{})
	go func() {
		defer close(b.done)
		b.svc.RunBuilds(ctx)
	}()
}

func (b *buildWorld) stopRunner() {
	if b.stop != nil {
		b.stop()
		<-b.done
		b.stop = nil
	}
}

func (b *buildWorld) setResult(f func(spec build.Spec) buildtest.Result) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.result = f
}

func ptrTo(s string) *string { return &s }

func (b *buildWorld) configure(a Actor, site string) {
	b.t.Helper()
	_, err := b.svc.SetSiteBuildSettings(context.Background(), a, site, SiteBuildSettingsInput{
		Command: "npm ci\nnpm run build", Output: "dist", Image: "node-24",
		Variables: []BuildVariableInput{{Name: "MODE", Value: ptrTo("production")}, {Name: "API_KEY", Value: ptrTo(buildSecret), Secret: true}},
	})
	if err != nil {
		b.t.Fatal(err)
	}
}

// pushCommit records a commit on main and delivers its push.
func (b *buildWorld) pushCommit(files map[string]string) string {
	b.t.Helper()
	sha := b.gh.SetFiles(b.site, "main", files)
	b.mustReceive(b.delivery("push", b.push(githubtest.Push{After: sha, Message: "Change"})), "handled", 2)
	return sha
}

// waitRun waits until the run of site with commit sha has ended (or has
// status want, if given).
func (b *buildWorld) waitRun(site, sha string, want ...string) Run {
	b.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range b.runs(site) {
			if r.Commit != sha {
				continue
			}
			if len(want) > 0 && r.Status == want[0] || len(want) == 0 && store.RunFinished(r.Status) {
				return r
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.t.Fatalf("run of %s at %s did not get there (%v): %+v", site, sha, want, b.runs(site))
	return Run{}
}

func (b *buildWorld) log(a Actor, site, id string) RunLog {
	b.t.Helper()
	l, err := b.svc.SiteRunLog(context.Background(), a, site, id, 0)
	if err != nil {
		b.t.Fatal(err)
	}
	return l
}

func TestRunnerBuildsAPushAndPublishes(t *testing.T) {
	b := newBuildWorld(t)
	b.configure(rootSess, "rootsite")
	files := map[string]string{"package.json": `{"scripts":{"build":"vite build"}}`, "src/main.js": "x"}
	sha := b.pushCommit(files)

	run := b.waitRun("rootsite", sha)
	if run.Status != RunSucceeded || run.Release == "" || run.FinishedAt == nil || run.Detail != "" {
		t.Fatalf("run: %+v", run)
	}
	if cur := b.svc.deploys.Current("rootsite"); cur != run.Release {
		t.Fatalf("live %q, run published %q", cur, run.Release)
	}
	// The build got the commit's files and the site's settings.
	specs := b.fake.Specs()
	var spec build.Spec
	for _, s := range specs {
		if s.Site == "rootsite" {
			spec = s
		}
	}
	if spec.Command != "npm ci\nnpm run build" || spec.Output != "dist" || spec.Env["MODE"] != "production" ||
		spec.Secrets["API_KEY"] != buildSecret {
		t.Fatalf("spec: %s %+v %v", spec.Command, spec.Env, len(spec.Secrets))
	}
	// The log is kept, with the secret blanked out.
	l := b.log(rootSess, "rootsite", run.ID)
	if !l.Done || l.Live || !strings.Contains(l.Text, "using ***\n") || strings.Contains(l.Text, buildSecret) {
		t.Fatalf("log: %+v", l)
	}
	// The token for the source could only read that repository.
	var sawRead, sawStatus bool
	for _, tok := range b.gh.Tokens() {
		if tok.Permissions["contents"] == "read" && len(tok.RepositoryIDs) == 1 && tok.RepositoryIDs[0] == b.site {
			sawRead = true
		}
		if tok.Permissions["statuses"] == "write" {
			sawStatus = true
			if len(tok.Permissions) != 1 || len(tok.RepositoryIDs) != 1 || tok.RepositoryIDs[0] != b.site {
				t.Errorf("status token: %+v", tok)
			}
		}
	}
	if !sawRead || !sawStatus {
		t.Errorf("tokens: %+v", b.gh.Tokens())
	}

	// bobsite has no settings: its run fails and says so.
	bobRun := b.waitRun("bobsite", sha)
	if bobRun.Status != RunFailed || !strings.Contains(bobRun.Detail, "build settings") {
		t.Fatalf("bobsite: %+v", bobRun)
	}

	// GitHub heard: pending and the outcome, per site, with a link. The
	// outcome is posted right after the run ended.
	for deadline := time.Now().Add(5 * time.Second); len(b.gh.Statuses()) < 4 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	var states []string
	for _, st := range b.gh.Statuses() {
		if st.SHA != sha || st.Context != "ownpanel" || !strings.HasPrefix(st.TargetURL, "https://panel.example.com:8443/#/sites/") {
			t.Errorf("status: %+v", st)
		}
		states = append(states, st.State)
	}
	if strings.Count(strings.Join(states, ","), "pending") != 2 || !strings.Contains(strings.Join(states, ","), "success") ||
		!strings.Contains(strings.Join(states, ","), "failure") {
		t.Errorf("states: %v", states)
	}
	// No secret in the panel's log, activity or GitHub.
	b.assertNoSecrets("log", b.logs.String())
	if strings.Contains(b.logs.String(), buildSecret) {
		t.Fatal("secret in the log")
	}
	acts, _ := b.svc.store.Activities(context.Background(), 100)
	if strings.Contains(asJSON(t, acts), buildSecret) {
		t.Fatal("secret in the activity log")
	}
}

func TestRunnerFailedBuildChangesNothing(t *testing.T) {
	b := newBuildWorld(t)
	b.configure(rootSess, "rootsite")
	sha1 := b.pushCommit(map[string]string{"index.html": "one"})
	first := b.waitRun("rootsite", sha1)
	if first.Status != RunSucceeded {
		t.Fatalf("first: %+v", first)
	}
	b.setResult(func(build.Spec) buildtest.Result {
		return buildtest.Result{State: build.StateFailed, Reason: build.ReasonExit, ExitCode: 2, Message: "exit status 2",
			Log: "npm ERR! missing script\n"}
	})
	sha2 := b.pushCommit(map[string]string{"index.html": "two"})
	second := b.waitRun("rootsite", sha2)
	if second.Status != RunFailed || !strings.Contains(second.Detail, "exit code 2") || second.Release != "" {
		t.Fatalf("second: %+v", second)
	}
	if cur := b.svc.deploys.Current("rootsite"); cur != first.Release {
		t.Fatalf("live changed to %q", cur)
	}
	if l := b.log(rootSess, "rootsite", second.ID); !strings.Contains(l.Text, "missing script") || !l.Done {
		t.Fatalf("log: %+v", l)
	}
}

func TestBuildNowLiveLogAndCancel(t *testing.T) {
	b := newBuildWorld(t)
	ctx := context.Background()
	// Without settings, Build now asks for them.
	if _, err := b.svc.StartSiteBuild(ctx, rootSess, "rootsite"); KindOf(err) != Conflict {
		t.Fatalf("no settings: %v", err)
	}
	b.configure(rootSess, "rootsite")
	sha := b.gh.SetFiles(b.site, "main", map[string]string{"index.html": "x"})
	hold := make(chan struct{})
	b.setResult(func(build.Spec) buildtest.Result {
		return buildtest.Result{Hold: hold, Running: "step 1 " + buildSecret + "\nstep 2\n", Log: "never\n",
			Output: buildtest.SiteArchive(map[string]string{"index.html": "x"})}
	})
	run, err := b.svc.StartSiteBuild(ctx, rootSess, "rootsite")
	if err != nil || run.Trigger != RunTriggerManual || run.Commit != sha || run.Pusher != "root" || run.Status != RunQueued {
		t.Fatalf("build now: %+v %v", run, err)
	}
	b.waitRun("rootsite", sha, RunRunning)
	// The log while it runs.
	deadline := time.Now().Add(5 * time.Second)
	var l RunLog
	for time.Now().Before(deadline) {
		l = b.log(rootSess, "rootsite", run.ID)
		if strings.Contains(l.Text, "step 2") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !l.Live || l.Done || l.Text != "step 1 ***\nstep 2\n" {
		t.Fatalf("live log: %+v", l)
	}
	// Reading on from where it was.
	if l2, _ := b.svc.SiteRunLog(ctx, rootSess, "rootsite", run.ID, l.Next); l2.Text != "" || l2.Offset != l.Next {
		t.Fatalf("next part: %+v", l2)
	}
	// Who may not see the site does not see the log; who may not change
	// it cannot cancel.
	if _, err := b.svc.SiteRunLog(ctx, bobSess, "rootsite", run.ID, 0); KindOf(err) != NotFound {
		t.Fatalf("bob reads root's log: %v", err)
	}
	if _, err := b.svc.SiteRunLog(ctx, bobSess, "bobsite", run.ID, 0); KindOf(err) != NotFound {
		t.Fatalf("another site's run: %v", err)
	}
	if _, err := b.svc.CancelSiteRun(ctx, rootRead, "rootsite", run.ID); KindOf(err) != Forbidden {
		t.Fatalf("read token cancels: %v", err)
	}
	before := b.svc.deploys.Current("rootsite")
	got, err := b.svc.CancelSiteRun(ctx, rootSess, "rootsite", run.ID)
	if err != nil || got.Status != RunCancelled || got.Detail != "cancelled by root" {
		t.Fatalf("cancel: %+v %v", got, err)
	}
	if b.svc.deploys.Current("rootsite") != before {
		t.Fatal("a cancelled build changed the site")
	}
	if l := b.log(rootSess, "rootsite", run.ID); !l.Done || !strings.Contains(l.Text, "step 2") || strings.Contains(l.Text, buildSecret) {
		t.Fatalf("log after cancel: %+v", l)
	}
	if _, err := b.svc.CancelSiteRun(ctx, rootSess, "rootsite", run.ID); KindOf(err) != Conflict {
		t.Fatalf("cancel twice: %v", err)
	}

	// A queued run is cancelled without a build.
	b.stopRunner()
	q, err := b.svc.StartSiteBuild(ctx, rootSess, "rootsite")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.svc.CancelSiteRun(ctx, rootSess, "rootsite", q.ID); err != nil || got.Status != RunCancelled {
		t.Fatalf("cancel queued: %+v %v", got, err)
	}

	// Members may build their own sites; not others'.
	if _, err := b.svc.StartSiteBuild(ctx, bobSess, "rootsite"); KindOf(err) != NotFound {
		t.Fatalf("bob builds root's site: %v", err)
	}
	b.configure(bobSess, "bobsite")
	if _, err := b.svc.StartSiteBuild(ctx, bobSess, "bobsite"); err != nil {
		t.Fatalf("bob builds his site: %v", err)
	}
	if _, err := b.svc.StartSiteBuild(ctx, rootRead, "rootsite"); KindOf(err) != Forbidden {
		t.Fatalf("read token: %v", err)
	}
	// Without a build service there is no Build now.
	b.svc.builds = nil
	if _, err := b.svc.StartSiteBuild(ctx, rootSess, "rootsite"); KindOf(err) != Unavailable {
		t.Fatalf("no builds: %v", err)
	}
}

func TestRunnerAfterRestart(t *testing.T) {
	b := newBuildWorld(t)
	b.stopRunner()
	b.configure(rootSess, "rootsite")
	ctx := context.Background()
	sha := b.gh.SetFiles(b.site, "main", map[string]string{"index.html": "x"})
	// A run that was being built when the panel stopped, and one queued.
	stale, err := b.svc.store.AddRun(ctx, store.Run{Site: "rootsite", Trigger: RunTriggerPush, SourceKind: store.SourceGitHub,
		RepositoryID: b.site, Repository: "acme/site", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40)}, "x", RunsKept)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.store.ClaimNextRun(ctx, "rootsite", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.store.AddRun(ctx, store.Run{Site: "rootsite", Trigger: RunTriggerPush, SourceKind: store.SourceGitHub,
		RepositoryID: b.site, Repository: "acme/site", Ref: "refs/heads/main", CommitSHA: sha}, "x", RunsKept); err != nil {
		t.Fatal(err)
	}
	b.startRunner()
	if r := b.waitRun("rootsite", sha); r.Status != RunSucceeded {
		t.Fatalf("queued run: %+v", r)
	}
	if r, _ := b.svc.store.Run(ctx, stale.ID); r.Status != RunFailed || !strings.Contains(r.Detail, "restarted") {
		t.Fatalf("stale run: %+v", r)
	}
}

func TestRunnerSourceChecks(t *testing.T) {
	b := newBuildWorld(t)
	b.configure(rootSess, "rootsite")
	ctx := context.Background()
	// A commit GitHub does not have.
	b.mustReceive(b.delivery("push", b.push(githubtest.Push{After: strings.Repeat("c", 40)})), "handled", 2)
	r := b.waitRun("rootsite", strings.Repeat("c", 40))
	if r.Status != RunFailed || !strings.Contains(r.Detail, "does not have the commit") {
		t.Fatalf("unknown commit: %+v", r)
	}
	// A run recorded for another repository than the one linked now.
	b.stopRunner()
	sha := b.gh.SetFiles(b.site, "main", map[string]string{"index.html": "x"})
	if _, err := b.svc.store.AddRun(ctx, store.Run{Site: "rootsite", Trigger: RunTriggerPush, SourceKind: store.SourceGitHub,
		RepositoryID: b.other, Repository: "acme/other", Ref: "refs/heads/main", CommitSHA: sha}, "x", RunsKept); err != nil {
		t.Fatal(err)
	}
	b.startRunner()
	if r := b.waitRun("rootsite", sha); r.Status != RunFailed || !strings.Contains(r.Detail, "another repository") {
		t.Fatalf("other repository: %+v", r)
	}
}

func TestBuildSettings(t *testing.T) {
	b := newBuildWorld(t)
	ctx := context.Background()
	got, err := b.svc.GetSiteBuildSettings(ctx, bobSess, "bobsite")
	if err != nil || got.Configured || len(got.Images) == 0 || !got.BuildsAvailable {
		t.Fatalf("empty: %+v %v", got, err)
	}
	b.configure(bobSess, "bobsite")
	got, err = b.svc.GetSiteBuildSettings(ctx, bobRead, "bobsite")
	if err != nil || !got.Configured || got.Command != "npm ci\nnpm run build" || len(got.Variables) != 2 ||
		got.Variables[0] != (BuildVariable{Name: "MODE", Value: "production"}) ||
		got.Variables[1] != (BuildVariable{Name: "API_KEY", Secret: true}) || got.UpdatedBy != "bob" {
		t.Fatalf("stored: %+v %v", got, err)
	}
	if strings.Contains(asJSON(t, got), buildSecret) {
		t.Fatal("secret in the answer")
	}
	// Stored sealed.
	raw, _ := b.svc.store.SiteBuildSettings(ctx, "bobsite")
	if strings.Contains(string(raw.Variables), buildSecret) || strings.Contains(string(raw.Variables), "production") {
		t.Fatal("variables stored in the clear")
	}
	// Saving again without the secret's value keeps it.
	if _, err := b.svc.SetSiteBuildSettings(ctx, bobSess, "bobsite", SiteBuildSettingsInput{Command: "npm run build", Output: "dist",
		Variables: []BuildVariableInput{{Name: "API_KEY", Secret: true}}}); err != nil {
		t.Fatal(err)
	}
	full, _, _ := b.svc.siteBuildSettings(ctx, "bobsite")
	if len(full.Env) != 1 || full.Env[0].Value != buildSecret || full.Image != "node-24" {
		t.Fatalf("kept: %v", full)
	}
	// Refusals; none repeats a value.
	for name, in := range map[string]SiteBuildSettingsInput{
		"new variable without value": {Output: "dist", Variables: []BuildVariableInput{{Name: "NEW"}}},
		"denied name":                {Output: "dist", Variables: []BuildVariableInput{{Name: "PATH", Value: ptrTo("/x-secret-path")}}},
		"short secret":               {Output: "dist", Variables: []BuildVariableInput{{Name: "K", Value: ptrTo("abc"), Secret: true}}},
		"output outside":             {Output: "../etc"},
		"unknown image":              {Command: "make", Output: "dist", Image: "ruby-3"},
	} {
		_, err := b.svc.SetSiteBuildSettings(ctx, bobSess, "bobsite", in)
		if KindOf(err) != Invalid || strings.Contains(err.Error(), "x-secret-path") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Permissions.
	if _, err := b.svc.SetSiteBuildSettings(ctx, bobRead, "bobsite", SiteBuildSettingsInput{Output: "."}); KindOf(err) != Forbidden {
		t.Errorf("read token: %v", err)
	}
	if _, err := b.svc.SetSiteBuildSettings(ctx, bobSess, "rootsite", SiteBuildSettingsInput{Output: "."}); KindOf(err) != NotFound {
		t.Errorf("bob on root's site: %v", err)
	}
	if _, err := b.svc.GetSiteBuildSettings(ctx, bobSess, "rootsite"); KindOf(err) != NotFound {
		t.Errorf("bob reads root's settings: %v", err)
	}
	// A plain folder: no command, published as it is through the build
	// service.
	if _, err := b.svc.SetSiteBuildSettings(ctx, rootSess, "rootsite", SiteBuildSettingsInput{Output: "."}); err != nil {
		t.Fatal(err)
	}
	sha := b.pushCommit(map[string]string{"index.html": "plain"})
	if r := b.waitRun("rootsite", sha); r.Status != RunSucceeded {
		t.Fatalf("plain folder: %+v", r)
	}
	acts, _ := b.svc.store.Activities(ctx, 100)
	if strings.Contains(asJSON(t, acts), buildSecret) || !strings.Contains(asJSON(t, acts), "site.build-settings") {
		t.Fatal("activity")
	}
}

func TestDetectBuildSettings(t *testing.T) {
	b := newBuildWorld(t)
	ctx := context.Background()
	b.gh.SetTree(b.site, "main", map[string]githubtest.File{
		"package.json":         {Body: `{"scripts":{"build":"vite build"},"devDependencies":{"vite":"^6.0.0"}}`},
		"package-lock.json":    {Body: "{}"},
		"vite.config.js":       {Body: "export default { build: { outDir: 'public-out' } }"},
		".nvmrc":               {Link: "/etc/passwd"},
		"sub/astro.config.mjs": {Body: "x"},
	})
	p, err := b.svc.DetectBuildSettings(ctx, bobSess, "bobsite")
	if err != nil {
		t.Fatal(err)
	}
	if p.Preset != "vite" || p.Settings.Output != "public-out" || !strings.Contains(p.Settings.Command, "npm ci") ||
		p.Branch != "main" || len(p.Commit) != 40 || !p.Buildable {
		t.Fatalf("%+v", p)
	}
	if _, err := b.svc.DetectBuildSettings(ctx, bobRead, "bobsite"); KindOf(err) != Forbidden {
		t.Errorf("read token: %v", err)
	}
	if _, err := b.svc.DetectBuildSettings(ctx, bobSess, "bobother"); KindOf(err) != Conflict {
		t.Errorf("not linked: %v", err)
	}
}

func TestRunLogLimits(t *testing.T) {
	l := newRunLog(map[string]string{"K": "line-one-secret\nline-two-secret", "S": "ab"})
	l.Write([]byte("a line-one-secret b\nc line-two"))
	l.Write([]byte("-secret d\npartial"))
	l.flush()
	text, tr := l.snapshot()
	if text != "a *** b\nc *** d\npartial" || tr {
		t.Fatalf("%q", text)
	}
	big := newRunLog(nil)
	line := strings.Repeat("x", 1023) + "\n"
	for i := 0; i < RunLogMax/len(line)+10; i++ {
		big.Write([]byte(line))
	}
	big.addTail("cut\nthe end\n")
	text, tr = big.snapshot()
	if !tr || len(text) > RunLogMax+len(runLogLeftOut)+20 || !strings.HasSuffix(text, runLogLeftOut+"the end\n") {
		t.Fatalf("truncated: %v %d", tr, len(text))
	}
}

func TestRunReporterIgnoresEndedRuns(t *testing.T) {
	b := newBuildWorld(t)
	b.stopRunner()
	r := runReporter{s: b.svc}
	r.Report(context.Background(), RunUpdate{RunID: "nope", Status: RunFailed})
	if err := errors.New("x"); errorMessage(err) != "the build failed" {
		t.Fatal(errorMessage(err))
	}
}
