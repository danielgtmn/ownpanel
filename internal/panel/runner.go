package panel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// The runner builds runs (#113, #115): it takes the queued runs of the
// sites, one per site at a time, and builds each with BuildCommit through
// the site's build settings. A run that succeeds makes its version live; a
// run that fails, is cancelled or runs out of time changes nothing.
//
//   - Runs come from pushes (github_webhook.go) and from "Build now"
//     (StartSiteBuild). A newer run replaces a queued one (the store does
//     that when it records it); a running one is left to finish.
//   - Statuses and times reach the run through runReporter (the panel's
//     RunReporter); the runner makes sure every run it took ends, also
//     when the build never started (no settings, source gone, …).
//   - The log of the build is kept with the run (runLog): live while it
//     builds, stored when it ends, limited to RunLogMax, with the values
//     of secret variables blanked out.
//   - Restarts: when the panel starts, runs still marked running have
//     lost their build (RemoveStaleBuilds removes it from the build
//     service) and are marked failed; queued runs are taken as usual.
//     Stopping the panel does not cancel builds: it ends with the process.
//   - GitHub hears about each run as a status on its commit (pending, then
//     success or failure) with a link to the site in the panel, when the
//     panel has a public address.

const (
	// RunLogMax is how much of a build's log is kept with its run; beyond
	// it, the end of the log (runLogTail) is kept after a note.
	RunLogMax  = 1 << 20
	runLogTail = 64 << 10
	// runLogChunk bounds one answer of RunLog.
	runLogChunk = 256 << 10
	// runnerPoll is how often the runner looks for queued runs without
	// being woken.
	runnerPoll = 15 * time.Second
	// sourceFetchTime bounds downloading a commit for a build.
	sourceFetchTime = 15 * time.Minute
	// commitStatusContext names the panel's status on GitHub commits.
	commitStatusContext = "ownpanel"
	// cancelWait is how long CancelSiteRun waits for a running build to
	// end before it answers.
	cancelWait = 15 * time.Second
)

const (
	runRestartedDetail = "the panel restarted while the site was being built; build it again"
	runReplacedDetail  = "replaced by a newer build"
	runNoSettings      = "choose how the site is built in its build settings first"
	runLogLeftOut      = "\n[… the middle of the log is longer than the panel keeps and was left out …]\n"
	runLogNotKept      = "The log is not kept: a secret value of the build is too short to be hidden in it.\n"
)

// buildRunner is the panel's state of the runs it builds.
type buildRunner struct {
	mu     sync.Mutex
	wake   chan struct{}
	active map[string]*activeRun // by run id
	sites  map[string]bool       // sites with a run being built here
	logs   map[string]*runLog    // by run id, while the build runs
	poll   time.Duration
}

type activeRun struct {
	site        string
	cancel      context.CancelFunc
	cancelledBy string
	done        chan struct{}
}

func newBuildRunner() *buildRunner {
	return &buildRunner{
		wake: make(chan struct{}, 1), active: map[string]*activeRun{}, sites: map[string]bool{},
		logs: map[string]*runLog{}, poll: runnerPoll,
	}
}

// wakeRunner asks the runner to look for queued runs now.
func (s *Service) wakeRunner() {
	select {
	case s.runner.wake <- struct{}{}:
	default:
	}
}

// RunBuilds builds queued runs until ctx is done. Start it once, after the
// store is open. On a panel without a build service it only marks the runs
// a previous start left running.
func (s *Service) RunBuilds(ctx context.Context) {
	if s.store == nil {
		return
	}
	if n, err := s.store.FailRunningRuns(ctx, runRestartedDetail); err != nil {
		slog.Error("could not mark the runs of an earlier start of the panel", "err", err)
	} else if n > 0 {
		slog.Info("marked runs failed that were being built when the panel stopped", "runs", n)
	}
	if s.builds == nil {
		return
	}
	tick := time.NewTicker(s.runner.poll)
	defer tick.Stop()
	for {
		s.startQueuedRuns(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.runner.wake:
		case <-tick.C:
		}
	}
}

// startQueuedRuns takes the newest queued run of every site that has none
// being built and builds it.
func (s *Service) startQueuedRuns(ctx context.Context) {
	sites, err := s.store.SitesWithQueuedRuns(ctx)
	if err != nil {
		slog.Error("could not list the queued runs", "err", err)
		return
	}
	for _, site := range sites {
		s.runner.mu.Lock()
		busy := s.runner.sites[site]
		s.runner.mu.Unlock()
		if busy {
			continue
		}
		run, err := s.store.ClaimNextRun(ctx, site, runSupersededDetail)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			slog.Error("could not take a queued run", "site", site, "err", err)
			continue
		}
		// Builds outlive the context they were started from: stopping the
		// panel leaves them to the next start (see above).
		bctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		ar := &activeRun{site: site, cancel: cancel, done: make(chan struct{})}
		s.runner.mu.Lock()
		s.runner.active[run.ID] = ar
		s.runner.sites[site] = true
		s.runner.mu.Unlock()
		go s.buildQueuedRun(bctx, run, ar)
	}
}

// buildQueuedRun builds one run the runner took.
func (s *Service) buildQueuedRun(ctx context.Context, run store.Run, ar *activeRun) {
	defer func() {
		ar.cancel()
		s.runner.mu.Lock()
		delete(s.runner.active, run.ID)
		delete(s.runner.sites, run.Site)
		s.runner.mu.Unlock()
		close(ar.done)
		s.wakeRunner() // the site may have a newer run waiting
	}()
	bg := context.WithoutCancel(ctx)
	s.setCommitStatus(bg, run, github.StatusPending, "Building the site")

	b, ok, err := s.siteBuildSettings(ctx, run.Site)
	if err == nil && !ok {
		err = errorf(Conflict, "%s", runNoSettings)
	}
	if err == nil {
		ref := SourceRef{Site: run.Site, Commit: run.CommitSHA, RepositoryID: run.RepositoryID}
		_, err = s.BuildCommit(ctx, System, ref, buildSettingsFor(b), true, run.ID)
	}
	// Every run the runner took ends, also when its build never started.
	cur, gerr := s.store.Run(bg, run.ID)
	if gerr == nil && !store.RunFinished(cur.Status) {
		status, detail := store.RunFailed, "the build could not start"
		if err != nil {
			detail = errorMessage(err)
		}
		if ctx.Err() != nil {
			status, detail = store.RunCancelled, s.cancelDetail(run.ID)
		}
		if _, ferr := s.store.FinishRun(bg, run.ID, status, plainText(detail, runMessageMax)); ferr != nil && !errors.Is(ferr, store.ErrConflict) {
			slog.Error("could not end a run", "site", run.Site, "run", run.ID, "err", ferr)
		}
	}
	if cur, gerr = s.store.Run(bg, run.ID); gerr != nil {
		return // the site went away meanwhile
	}
	switch cur.Status {
	case store.RunSucceeded:
		s.setCommitStatus(bg, cur, github.StatusSuccess, "Published as version "+cur.Release)
	case store.RunCancelled, store.RunSkipped:
		s.setCommitStatus(bg, cur, github.StatusError, "Cancelled: "+cur.Detail)
	default:
		s.setCommitStatus(bg, cur, github.StatusFailure, "Failed: "+cur.Detail)
	}
}

// errorMessage is an error for people: the panel's own message (which
// never carries a secret), else a general one.
func errorMessage(err error) string {
	var pe *Error
	if errors.As(err, &pe) && pe.Msg != "" {
		return pe.Msg
	}
	return "the build failed"
}

func (s *Service) cancelDetail(runID string) string {
	s.runner.mu.Lock()
	defer s.runner.mu.Unlock()
	if ar := s.runner.active[runID]; ar != nil && ar.cancelledBy != "" {
		return "cancelled by " + ar.cancelledBy
	}
	return "cancelled"
}

// ---- RunReporter ----

// runReporter writes the end of builds to their runs; a run is already
// running when its build is started (the runner took it), and the final
// update ends it. It also passes every update on to Config.Runs.
type runReporter struct {
	s     *Service
	extra RunReporter
}

func (r runReporter) Report(ctx context.Context, u RunUpdate) {
	if r.extra != nil {
		r.extra.Report(ctx, u)
	}
	if u.RunID == "" || r.s.store == nil {
		return
	}
	switch u.Status {
	case RunSucceeded, RunFailed, RunCancelled, RunSkipped:
	default:
		return
	}
	detail := u.Message
	switch u.Status {
	case RunSucceeded:
		detail = ""
	case RunCancelled:
		detail = r.s.cancelDetail(u.RunID)
	case RunFailed:
		if u.Reason == build.ReasonExit {
			detail = fmt.Sprintf("the build command ended with exit code %d", u.ExitCode)
		}
	}
	if _, err := r.s.store.FinishRunWithRelease(ctx, u.RunID, u.Status, plainText(detail, runMessageMax), u.Release); err != nil &&
		!errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
		slog.Error("could not end a run", "site", u.Site, "run", u.RunID, "err", err)
	}
}

// ---- SourceFetcher ----

// githubSources fetches the commit of a build from the site's linked
// GitHub repository with a token that may only read that repository.
type githubSources struct{ s *Service }

func (g githubSources) FetchSource(ctx context.Context, ref SourceRef) (io.ReadCloser, int, error) {
	src, v, err := g.s.linkedGitHubSource(ctx, ref.Site)
	if err != nil {
		return nil, 0, err
	}
	if ref.RepositoryID != 0 && src.RepositoryID != ref.RepositoryID {
		return nil, 0, errorf(Conflict, "the site is linked to another repository now")
	}
	_, app, err := g.s.installationApp(ctx, src.InstallationID)
	if err != nil {
		return nil, 0, err
	}
	tok, err := g.s.githubReadToken(ctx, src.InstallationID, src.RepositoryID)
	if err != nil {
		return nil, 0, err
	}
	owner, name := repoParts(v.Repository)
	dctx, cancel := context.WithTimeout(ctx, sourceFetchTime)
	rc, err := g.s.github.Tarball(dctx, tok, owner, name, ref.Commit, build.MaxSourceStreamBytes)
	if err != nil {
		cancel()
		if errors.Is(err, github.ErrNotFound) {
			return nil, 0, &Error{Kind: Conflict, Msg: "GitHub does not have the commit (any more)", Err: err}
		}
		return nil, 0, githubError(app.Name, err)
	}
	return &cancelOnClose{ReadCloser: rc, cancel: cancel}, 1, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// ---- Build now and Cancel ----

// StartSiteBuild records a run of the newest commit of the site's linked
// branch ("Build now") and wakes the runner. A queued run of the site is
// replaced; one that is being built finishes first.
func (s *Service) StartSiteBuild(ctx context.Context, a Actor, site string) (Run, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return Run{}, err
	}
	if err := needWrite(a); err != nil {
		return Run{}, err
	}
	if !s.PublishingAvailable() {
		return Run{}, errorf(Unavailable, "this server does not build sites")
	}
	if _, ok, err := s.siteBuildSettings(ctx, st.Name); err != nil {
		return Run{}, err
	} else if !ok {
		return Run{}, errorf(Conflict, "%s", runNoSettings)
	}
	src, v, err := s.linkedGitHubSource(ctx, st.Name)
	if err != nil {
		return Run{}, err
	}
	_, app, err := s.installationApp(ctx, src.InstallationID)
	if err != nil {
		return Run{}, err
	}
	tok, err := s.githubReadToken(ctx, src.InstallationID, src.RepositoryID)
	if err != nil {
		return Run{}, err
	}
	owner, name := repoParts(v.Repository)
	gctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	br, err := s.github.GetBranch(gctx, tok, owner, name, src.Branch)
	if errors.Is(err, github.ErrNotFound) {
		return Run{}, errorf(Conflict, "%s has no branch %q any more", v.Repository, src.Branch)
	}
	if err != nil {
		return Run{}, githubError(app.Name, err)
	}
	if len(br.SHA) != 40 {
		return Run{}, errorf(Conflict, "the panel cannot build repositories with this kind of commit id yet")
	}
	r, err := s.store.AddRun(ctx, store.Run{
		Site: st.Name, Trigger: RunTriggerManual, SourceKind: store.SourceGitHub, RepositoryID: src.RepositoryID,
		Repository: v.Repository, Ref: "refs/heads/" + src.Branch, CommitSHA: br.SHA, Pusher: a.label(),
	}, runReplacedDetail, RunsKept)
	if err != nil {
		return Run{}, storeError(err)
	}
	s.record(ctx, a, "site.build-start", st.Name, fmt.Sprintf("%s, branch %s, commit %s", v.Repository, src.Branch, br.SHA[:7]))
	s.wakeRunner()
	return s.viewRun(r), nil
}

// CancelSiteRun cancels a queued run, or stops the build of a running one
// (it ends cancelled; the site is unchanged).
func (s *Service) CancelSiteRun(ctx context.Context, a Actor, site, runID string) (Run, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return Run{}, err
	}
	if err := needWrite(a); err != nil {
		return Run{}, err
	}
	r, err := s.store.Run(ctx, runID)
	if err != nil || r.Site != st.Name {
		return Run{}, errorf(NotFound, "run not found")
	}
	who := a.label()
	switch r.Status {
	case store.RunQueued:
		if _, err := s.store.FinishRun(ctx, r.ID, store.RunCancelled, plainText("cancelled by "+who, runMessageMax)); err != nil {
			return Run{}, storeError(err)
		}
	case store.RunRunning:
		s.runner.mu.Lock()
		ar := s.runner.active[r.ID]
		if ar != nil {
			ar.cancelledBy = who
			ar.cancel()
		}
		s.runner.mu.Unlock()
		if ar == nil {
			// Nothing here builds it (the panel restarted): just end it.
			if _, err := s.store.FinishRun(ctx, r.ID, store.RunCancelled, plainText("cancelled by "+who, runMessageMax)); err != nil {
				return Run{}, storeError(err)
			}
			break
		}
		select {
		case <-ar.done:
		case <-time.After(cancelWait):
		case <-ctx.Done():
		}
	default:
		return Run{}, errorf(Conflict, "the run has already ended")
	}
	s.record(ctx, a, "site.build-cancel", st.Name, runView(r).ShortCommit)
	r, err = s.store.Run(context.WithoutCancel(ctx), r.ID)
	if err != nil {
		return Run{}, storeError(err)
	}
	return s.viewRun(r), nil
}

// ---- logs ----

// RunLog is a part of a run's log.
type RunLog struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
	// Text is the log from Offset on, at most a few hundred KB; it is what
	// the build printed (secrets blanked out): show it as text only.
	Text   string `json:"text"`
	Offset int64  `json:"offset"`
	// Next is the offset to ask for next.
	Next int64 `json:"next"`
	// Done: the run has ended and Text reaches the end of its log.
	Done bool `json:"done"`
	// Truncated: the log was longer than the panel keeps; the middle is
	// left out.
	Truncated bool `json:"truncated"`
	// Live: the build is running and the log grows.
	Live bool `json:"live"`
}

// SiteRunLog returns a run's log from offset on (bytes), for whoever may
// see the site. While the build runs, it is what arrived so far.
func (s *Service) SiteRunLog(ctx context.Context, a Actor, site, runID string, offset int64) (RunLog, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return RunLog{}, err
	}
	r, err := s.store.Run(ctx, runID)
	if err != nil || r.Site != st.Name {
		return RunLog{}, errorf(NotFound, "run not found")
	}
	out := RunLog{RunID: r.ID, Status: r.Status, Offset: max(offset, 0)}
	s.runner.mu.Lock()
	l := s.runner.logs[r.ID]
	s.runner.mu.Unlock()
	var text string
	if l != nil {
		text, out.Truncated = l.snapshot()
		out.Live = true
	} else {
		text, out.Truncated, err = s.store.RunLog(ctx, r.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return RunLog{}, storeError(err)
		}
	}
	if out.Offset > int64(len(text)) {
		out.Offset = int64(len(text))
	}
	end := min(out.Offset+runLogChunk, int64(len(text)))
	out.Text = strings.ToValidUTF8(text[out.Offset:end], "?")
	out.Next = end
	out.Done = !out.Live && store.RunFinished(r.Status) && end == int64(len(text))
	return out, nil
}

// runLog is the log of a build while it runs: complete lines, secrets
// blanked out, at most RunLogMax.
type runLog struct {
	mu        sync.Mutex
	secrets   []string
	text      []byte
	pending   []byte
	raw       int64 // bytes received from the build service
	truncated bool
}

// newRunLog masks secrets line by line: a secret of several lines is
// blanked out line by line (each line of MinSecretMask or more).
func newRunLog(secrets map[string]string) *runLog {
	l := &runLog{}
	for _, v := range secrets {
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimRight(line, "\r")
			if len(line) >= build.MinSecretMask {
				l.secrets = append(l.secrets, line)
			}
		}
	}
	return l
}

func (l *runLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.raw += int64(len(p))
	if l.truncated {
		return len(p), nil
	}
	l.pending = append(l.pending, p...)
	for {
		i := strings.IndexByte(string(l.pending), '\n')
		if i < 0 {
			break
		}
		l.addLine(l.pending[:i+1])
		l.pending = l.pending[i+1:]
	}
	if len(l.pending) > 64<<10 {
		l.addLine(l.pending)
		l.pending = nil
	}
	return len(p), nil
}

// addLine appends one line; call with l.mu held.
func (l *runLog) addLine(b []byte) {
	if l.truncated {
		return
	}
	line := build.MaskSecrets(strings.ToValidUTF8(string(b), "?"), l.secrets)
	if len(l.text)+len(line) > RunLogMax {
		l.truncated = true
		return
	}
	l.text = append(l.text, line...)
}

// flush adds what is left of the last line.
func (l *runLog) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pending) > 0 {
		l.addLine(l.pending)
		l.pending = nil
	}
}

func (l *runLog) snapshot() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.text), l.truncated
}

func (l *runLog) received() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.raw
}

// addTail appends the end of a truncated log after a note.
func (l *runLog) addTail(tail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tail = strings.ToValidUTF8(tail, "?")
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:] // drop the cut first line
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(tail, "\n") {
		b.WriteString(build.MaskSecrets(line, l.secrets))
	}
	l.text = append(l.text, runLogLeftOut...)
	l.text = append(l.text, b.String()...)
}

// ---- the log of a build, kept with its run ----

// followLog starts keeping the log of r's build (a run's build that runs).
func (r *buildRun) followLog() {
	if r.req.RunID == "" {
		return
	}
	for _, v := range r.spec.Secrets {
		if len(v) < build.MinSecretMask {
			r.logNotKept = true
			return
		}
	}
	l := newRunLog(r.spec.Secrets)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.log, r.logStop, r.logDone = l, cancel, done
	runner := r.s.runner
	runner.mu.Lock()
	runner.logs[r.req.RunID] = l
	runner.mu.Unlock()
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			rc, err := r.s.builds.Log(ctx, r.spec.ID, l.received(), true)
			if err == nil {
				_, err = io.Copy(l, rc)
				rc.Close()
				if err == nil {
					return // the build has ended
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
}

// keepLog stores the log of r's build with its run: what was followed,
// whatever came after, and the end of a truncated log. It runs before the
// run's last status is reported, so a run that has ended has its log.
func (r *buildRun) keepLog() {
	if r.req.RunID == "" || (r.log == nil && !r.logNotKept) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if r.logNotKept {
		if err := r.s.store.PutRunLog(ctx, r.req.RunID, runLogNotKept, false); err != nil && !errors.Is(err, store.ErrNotFound) {
			slog.Error("could not store the log of a run", "run", r.req.RunID, "err", err)
		}
		return
	}
	l := r.log
	select {
	case <-r.logDone:
	case <-time.After(5 * time.Second):
	}
	r.logStop()
	<-r.logDone
	// Anything the stream missed (it ends with the build).
	if rc, err := r.s.builds.Log(ctx, r.spec.ID, l.received(), false); err == nil {
		_, _ = io.Copy(l, io.LimitReader(rc, int64(RunLogMax)+runLogTail))
		rc.Close()
	}
	l.flush()
	if _, truncated := l.snapshot(); truncated {
		off := max(l.received()-runLogTail, 0)
		if rc, err := r.s.builds.Log(ctx, r.spec.ID, off, false); err == nil {
			tail, _ := io.ReadAll(io.LimitReader(rc, runLogTail))
			rc.Close()
			l.addTail(string(tail))
		}
	}
	text, truncated := l.snapshot()
	if err := r.s.store.PutRunLog(ctx, r.req.RunID, text, truncated); err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Error("could not store the log of a run", "run", r.req.RunID, "err", err)
	}
	runner := r.s.runner
	runner.mu.Lock()
	delete(runner.logs, r.req.RunID)
	runner.mu.Unlock()
}

// ---- commit statuses on GitHub ----

// setCommitStatus reports a run to GitHub as a status of its commit, with
// a token that may only write statuses of that one repository. Failures
// are logged: the run itself is not affected.
func (s *Service) setCommitStatus(ctx context.Context, r store.Run, state, description string) {
	if r.SourceKind != store.SourceGitHub {
		return
	}
	src, err := s.store.SiteSource(ctx, r.Site)
	if err != nil || src.Kind != store.SourceGitHub || src.InstallationID == 0 || src.RepositoryID != r.RepositoryID {
		return
	}
	inst, app, err := s.installationApp(ctx, src.InstallationID)
	if err != nil || inst.Suspended {
		return
	}
	auth, err := s.appAuth(app)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	tok, err := auth.InstallationToken(ctx, src.InstallationID, github.TokenRequest{
		RepositoryIDs: []int64{src.RepositoryID}, Permissions: map[string]string{"statuses": "write"},
	})
	if err != nil {
		slog.Info("could not report a build to GitHub", "site", r.Site, "run", r.ID, "err", err)
		return
	}
	st := github.CommitStatus{State: state, Description: plainText(description, 140), Context: commitStatusContext}
	if base, err := s.githubBase(); err == nil {
		st.TargetURL = base + "/#/sites/" + url.PathEscape(r.Site) + "?run=" + url.QueryEscape(r.ID)
	}
	owner, name := repoParts(src.Repository)
	if err := s.github.SetCommitStatus(ctx, tok, owner, name, r.CommitSHA, st); err != nil {
		slog.Info("could not report a build to GitHub", "site", r.Site, "run", r.ID, "err", err)
	}
}
