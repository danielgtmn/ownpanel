package panel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielgtmn/ownpanel/internal/build"
	"github.com/danielgtmn/ownpanel/internal/deploy"
)

// Building a site (#113): the source tree goes to the build service, the
// build runs there, and its output is published like an uploaded archive.
//
// The service runs code nobody reviewed, so nothing it hands back is
// trusted: the output goes through the same checks as an upload
// (internal/deploy), with a limit on the stream on top. A build that fails,
// is cancelled or runs out of time never touches the site's versions; a
// build that succeeds adds exactly one version, through PublishVersion's
// path. Whatever happens, the build is removed from the service afterwards.
//
// Two front doors feed this today: an administrator's uploaded source
// archive (BuildSiteFromUpload) and, once a site has a repository
// (#122/#125), BuildCommit through a SourceFetcher. Runs (#130) hear about
// each step through a RunReporter.

// Run states as reported to a RunReporter. They are the states of a run
// (#130): queued, running and one of the last four.
const (
	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
	// RunSkipped: a newer build of the same site replaced this one before
	// it started.
	RunSkipped = "skipped"
)

// RunUpdate is one step of a build as a RunReporter hears it. It never
// carries the build's variables.
type RunUpdate struct {
	// RunID is the caller's id of the run (BuildRequest.RunID); "" for
	// builds that are not tracked as runs.
	RunID string
	Site  string
	// Status is one of the Run* constants.
	Status string
	// Reason is a build.Reason* code when the build failed, "timeout" when
	// the panel stopped waiting, "replaced" when skipped. Message says it
	// for people.
	Reason  string
	Message string
	// ExitCode is the build command's, when Reason is build.ReasonExit.
	ExitCode int
	// Release is the new version after success; Live whether it was made
	// live.
	Release string
	Live    bool
	// QueuedAt is set from the first update on, StartedAt once the build
	// runs, FinishedAt with the last update.
	QueuedAt   time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

// RunReporter receives the steps of builds: queued, running (once the
// service runs it) and exactly one final status. Report must not block for
// long; it is called with a context that outlives the caller's
// cancellation. The runs of #130 implement it (store the status and
// timestamps of the run with RunUpdate.RunID).
type RunReporter interface {
	Report(ctx context.Context, u RunUpdate)
}

type noRuns struct{}

func (noRuns) Report(context.Context, RunUpdate) {}

// SourceRef names the source of a build in a repository.
type SourceRef struct {
	// Site is the site the source belongs to; the fetcher uses the
	// repository linked to it (#125), never one the caller names.
	Site string
	// Commit is the full id of the commit to build.
	Commit string
	// RepositoryID, when not 0, is the repository the commit is from: the
	// fetch fails if the site is linked to another one by now.
	RepositoryID int64
}

// SourceFetcher returns the source tree of a commit (#122/#125 implement it
// with a read-only token for the site's repository). Tree is a tar.gz
// stream of plain files, folders and links; Strip is how many leading
// folders every name has (1 for GitHub's archive of a commit). The stream
// is untrusted: the build service checks and limits it.
type SourceFetcher interface {
	FetchSource(ctx context.Context, ref SourceRef) (tree io.ReadCloser, strip int, err error)
}

// BuildSettings say how a site is built.
type BuildSettings struct {
	// Command runs in the source tree with sh -e, e.g. "npm ci\nnpm run build".
	Command string `json:"command"`
	// Output is the folder with the result, relative to the source tree;
	// "." is the tree itself.
	Output string `json:"output"`
	// Image is the name of a build image (build.Images), e.g. "node-24".
	Image string `json:"image"`
	// Env are environment variables; Secrets more of them whose values are
	// blanked out of the build's log. The values of neither appear in the
	// panel's log, activity entries, errors or answers.
	Env     map[string]string `json:"env,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

// String and GoString keep the command and the variables out of logs.
func (b BuildSettings) String() string {
	return fmt.Sprintf("build settings (image %s, output %s, %d variables)", b.Image, b.Output, len(b.Env)+len(b.Secrets))
}

// GoString is String.
func (b BuildSettings) GoString() string { return b.String() }

// BuildRequest is one build of a site.
type BuildRequest struct {
	Site string
	// Tree is the source tree as tar or tar.gz (untrusted); Strip leading
	// folders are dropped from every name.
	Tree     io.Reader
	Strip    int
	Settings BuildSettings
	// Live makes the new version live at once; without, it is added as a
	// preview to be made live later.
	Live bool
	// RunID is passed on to the RunReporter.
	RunID string
}

// BuildResult is a build after it ended.
type BuildResult struct {
	// Status is RunSucceeded, RunFailed, RunCancelled or RunSkipped.
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Message  string `json:"message,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	// LogTail is the end of the build's log (secrets blanked out); empty
	// when a secret is too short to be blanked out reliably.
	LogTail string `json:"log_tail,omitempty"`
	// Publication is the published version after success.
	Publication *Publication `json:"publication,omitempty"`
}

// Error codes of builds (Error.Code).
const (
	// CodeBuildFailed: the build ran and failed, or its output could not
	// be published; the site is unchanged.
	CodeBuildFailed = "build_failed"
	// CodeBuildReplaced: a newer build of the site replaced this one
	// before it started.
	CodeBuildReplaced = "build_replaced"
)

const (
	// buildLogTail is how much of the end of the log a result carries.
	buildLogTail = 8 << 10
	// buildCleanupTime bounds removing a build from the service.
	buildCleanupTime = time.Minute
)

// buildConfig is the part of Service that runs builds.
type buildConfig struct {
	runs    RunReporter
	sources SourceFetcher
	line    buildLine
	// ids numbers builds. It starts at the time the panel started, in
	// nanoseconds, so a number is never reused while the service may still
	// know a build of an earlier run of the panel; idBase tells which
	// builds the service knows from before (RemoveStaleBuilds).
	idMu   sync.Mutex
	nextID uint64
	idBase uint64
	// poll is how often a build's state is asked for; timeout how long
	// the panel waits for a build from its start (queue included) before
	// cancelling it. Fields so tests can lower them.
	poll    time.Duration
	timeout time.Duration
}

func newBuildConfig(runs RunReporter, sources SourceFetcher, now time.Time) buildConfig {
	if runs == nil {
		runs = noRuns{}
	}
	base := uint64(now.UnixNano())
	return buildConfig{
		runs: runs, sources: sources,
		nextID: base, idBase: base,
		line:    buildLine{sites: map[string]*siteLine{}},
		poll:    time.Second,
		timeout: time.Duration(build.MaxLimits.Seconds)*time.Second + 30*time.Minute,
	}
}

func (b *buildConfig) newID() uint64 {
	b.idMu.Lock()
	defer b.idMu.Unlock()
	b.nextID++
	return b.nextID
}

// BuildSiteFromUpload builds a site from an uploaded source archive and
// publishes the output. Until sites have their own build settings (#114)
// only administrators may run builds this way: a build runs arbitrary code
// with the server's processors, memory and network.
func (s *Service) BuildSiteFromUpload(ctx context.Context, a Actor, site string, tree io.Reader, settings BuildSettings, live bool) (BuildResult, error) {
	if err := needAdmin(a); err != nil {
		return BuildResult{}, err
	}
	return s.BuildSite(ctx, a, BuildRequest{Site: site, Tree: tree, Settings: settings, Live: live})
}

// BuildCommit builds a commit of the site's repository through the
// SourceFetcher (Config.Sources) and publishes the output.
func (s *Service) BuildCommit(ctx context.Context, a Actor, ref SourceRef, settings BuildSettings, live bool, runID string) (BuildResult, error) {
	if _, err := s.visibleSite(ctx, a, ref.Site); err != nil {
		return BuildResult{}, err
	}
	if err := needWrite(a); err != nil {
		return BuildResult{}, err
	}
	if s.build.sources == nil {
		return BuildResult{}, errorf(Unavailable, "this panel cannot fetch the source of sites yet")
	}
	tree, strip, err := s.build.sources.FetchSource(ctx, ref)
	if err != nil {
		// The panel's own fetcher fails with the panel's errors, whose
		// messages never carry a secret; anything else stays unsaid.
		msg := "the source could not be fetched"
		var pe *Error
		if errors.As(err, &pe) && pe.Msg != "" {
			msg += ": " + pe.Msg
		}
		return BuildResult{}, &Error{Kind: Unavailable, Msg: msg, Err: err}
	}
	defer tree.Close()
	return s.BuildSite(ctx, a, BuildRequest{Site: ref.Site, Tree: tree, Strip: strip, Settings: settings, Live: live, RunID: runID})
}

// BuildSite builds a site from a source tree and publishes the output as a
// new version, live or not. It returns when the build has ended (or ctx is
// done, which cancels it). Who may publish to the site may build it.
//
// Only one build of a site runs at a time; a request that comes while one
// runs waits, and a newer request replaces one that waits (it ends with
// RunSkipped and a Conflict error with CodeBuildReplaced). A build that
// fails ends with RunFailed and an Invalid error with CodeBuildFailed; the
// result still describes it. The site's versions change only on success.
func (s *Service) BuildSite(ctx context.Context, a Actor, req BuildRequest) (BuildResult, error) {
	site, err := s.visibleSite(ctx, a, req.Site)
	if err != nil {
		return BuildResult{}, err
	}
	if err := needWrite(a); err != nil {
		return BuildResult{}, err
	}
	if s.builds == nil {
		return BuildResult{}, errorf(Unavailable, "builds are not set up on this server")
	}
	spec := build.Spec{
		ID: s.build.newID(), Site: site.Name, Image: req.Settings.Image,
		Command: req.Settings.Command, Output: req.Settings.Output,
		Env: req.Settings.Env, Secrets: req.Settings.Secrets,
	}
	if err := spec.Validate(); err != nil {
		return BuildResult{}, errorf(Invalid, "%v", err)
	}
	if _, ok := build.FindImage(build.Images, spec.Image); !ok {
		return BuildResult{}, errorf(Invalid, "there is no build image called %q (%s)", spec.Image, imageNames())
	}

	r := &buildRun{s: s, a: a, req: req, spec: spec, update: RunUpdate{
		RunID: req.RunID, Site: site.Name, Status: RunQueued, QueuedAt: s.now(),
	}}
	r.report()
	// Take the place in the site's line first, so of two requests the
	// later one wins even if its source arrives first.
	t := s.build.line.enter(site.Name)
	defer r.remove() // after leave: the next build need not wait for it
	defer s.build.line.leave(site.Name, t)
	return r.run(ctx, t)
}

func imageNames() string {
	names := make([]string, 0, len(build.Images))
	for _, img := range build.Images {
		names = append(names, img.Name)
	}
	return "one of " + strings.Join(names, ", ")
}

// buildRun is one BuildSite call.
type buildRun struct {
	s       *Service
	a       Actor
	req     BuildRequest
	spec    build.Spec
	update  RunUpdate
	created bool
	// The log of a run's build, kept with the run (runner.go).
	log        *runLog
	logStop    context.CancelFunc
	logDone    chan struct{}
	logNotKept bool
}

func (r *buildRun) report() {
	r.s.build.runs.Report(context.WithoutCancel(context.Background()), r.update)
}

// remove deletes the build from the service, even when the caller's
// context is done.
func (r *buildRun) remove() {
	if !r.created {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildCleanupTime)
	defer cancel()
	if err := r.s.builds.Remove(ctx, r.spec.ID); err != nil {
		slog.Warn("removing a build from the build service failed", "site", r.spec.Site, "build", r.spec.ID, "err", err)
	}
}

// end sends the final update and returns the result with its error.
func (r *buildRun) end(status, reason, message string, err error) (BuildResult, error) {
	r.update.Status, r.update.Reason, r.update.Message = status, reason, message
	r.update.FinishedAt = r.s.now()
	r.keepLog()
	r.report()
	res := BuildResult{Status: status, Reason: reason, Message: message, ExitCode: r.update.ExitCode}
	if status != RunSkipped {
		slog.Info("build ended", "site", r.spec.Site, "build", r.spec.ID, "status", status, "reason", reason)
	}
	if status == RunFailed {
		r.s.record(context.WithoutCancel(context.Background()), r.a, "site.build-failed", r.spec.Site, reason)
	}
	return res, err
}

func (r *buildRun) fail(reason, message string) (BuildResult, error) {
	return r.end(RunFailed, reason, message, &Error{Kind: Invalid, Code: CodeBuildFailed, Msg: "the build failed: " + message})
}

// serviceError turns an answer of the build service into the panel's
// error; its messages never repeat the build's variables.
func serviceError(err error) error {
	var be *build.Error
	if errors.As(err, &be) {
		switch be.Status {
		case http.StatusBadRequest:
			return &Error{Kind: Invalid, Msg: be.Message, Err: err}
		case http.StatusTooManyRequests:
			return &Error{Kind: Throttled, Msg: "the build service is busy: " + be.Message + "; try again later", Err: err}
		case http.StatusRequestEntityTooLarge:
			return &Error{Kind: TooLarge, Msg: be.Message, Err: err}
		case http.StatusInsufficientStorage:
			return &Error{Kind: NoSpace, Msg: be.Message, Err: err}
		case http.StatusServiceUnavailable:
			return &Error{Kind: Unavailable, Msg: be.Message, Err: err}
		}
		return &Error{Kind: Unavailable, Msg: "the build service failed: " + be.Message, Err: err}
	}
	return &Error{Kind: Unavailable, Msg: "the build service does not answer", Err: err}
}

func (r *buildRun) run(ctx context.Context, t *lineTicket) (BuildResult, error) {
	svc := r.s.builds
	if _, err := svc.Create(ctx, r.spec); err != nil {
		if ctx.Err() != nil {
			// It may exist anyway; remove cleans it up.
			r.created = true
			return r.end(RunCancelled, "", "cancelled", errorf(Conflict, "the build was cancelled"))
		}
		e := serviceError(err)
		return r.end(RunFailed, "", e.Error(), e)
	}
	r.created = true
	if _, err := svc.Source(ctx, r.spec.ID, r.req.Tree, r.req.Strip); err != nil {
		if ctx.Err() != nil {
			return r.end(RunCancelled, "", "cancelled", errorf(Conflict, "the build was cancelled"))
		}
		var be *build.Error
		if errors.As(err, &be) && (be.Status == http.StatusBadRequest || be.Status == http.StatusRequestEntityTooLarge) {
			e := serviceError(err)
			return r.end(RunFailed, build.ReasonSource, be.Message, e)
		}
		e := serviceError(err)
		return r.end(RunFailed, "", e.Error(), e)
	}

	switch err := r.s.build.line.wait(ctx, t); {
	case errors.Is(err, errReplaced):
		return r.end(RunSkipped, "replaced", "a newer build of the site replaced this one",
			&Error{Kind: Conflict, Code: CodeBuildReplaced, Msg: "a newer build of this site replaced this one before it started"})
	case err != nil:
		return r.end(RunCancelled, "", "cancelled", errorf(Conflict, "the build was cancelled"))
	}

	wctx, cancel := context.WithTimeout(ctx, r.s.build.timeout)
	defer cancel()
	if _, err := svc.Start(wctx, r.spec.ID); err != nil {
		// Cancelled while the service was starting it: it may be running
		// already, so stop it; remove does the rest.
		if ctx.Err() != nil {
			stop, cancelStop := context.WithTimeout(context.Background(), buildCleanupTime)
			_, _ = svc.Cancel(stop, r.spec.ID)
			cancelStop()
			return r.end(RunCancelled, "", "cancelled", errorf(Conflict, "the build was cancelled"))
		}
		e := serviceError(err)
		return r.end(RunFailed, "", e.Error(), e)
	}
	r.followLog()
	b, err := r.wait(wctx)
	if err != nil {
		// Stop it now; remove does the rest.
		stop, cancelStop := context.WithTimeout(context.Background(), buildCleanupTime)
		_, _ = svc.Cancel(stop, r.spec.ID)
		cancelStop()
		switch {
		case ctx.Err() != nil:
			return r.end(RunCancelled, "", "cancelled", errorf(Conflict, "the build was cancelled"))
		case wctx.Err() != nil:
			return r.fail(build.ReasonTimeout, "the build did not end in time")
		case build.IsNotFound(err):
			return r.fail(build.ReasonInternal, "the build service restarted and lost the build")
		}
		e := serviceError(err)
		return r.end(RunFailed, "", e.Error(), e)
	}
	r.update.ExitCode = b.ExitCode
	switch b.State {
	case build.StateCancelled:
		return r.withLog(r.end(RunCancelled, "", "cancelled", errorf(Conflict, "the build was cancelled")))
	case build.StateFailed:
		msg := cleanDetail(b.Message)
		if msg == "" {
			msg = "reason: " + b.Reason
		}
		return r.withLog(r.fail(cleanDetail(b.Reason), msg))
	}
	return r.withLog(r.publish(ctx))
}

// wait polls the build until it has ended.
func (r *buildRun) wait(ctx context.Context) (build.Build, error) {
	tick := time.NewTicker(r.s.build.poll)
	defer tick.Stop()
	for {
		b, err := r.s.builds.Get(ctx, r.spec.ID)
		if err != nil {
			return build.Build{}, err
		}
		// A build that ended before it was seen running did run (if it
		// has a start time): the reporter hears every step.
		if r.update.Status == RunQueued && (b.State == build.StateRunning || (build.Finished(b.State) && b.StartedAt != "")) {
			r.update.Status = RunRunning
			r.update.StartedAt = r.s.now()
			if t, err := time.Parse(time.RFC3339, b.StartedAt); err == nil {
				r.update.StartedAt = t
			}
			r.report()
		}
		if build.Finished(b.State) {
			return b, nil
		}
		select {
		case <-ctx.Done():
			return build.Build{}, ctx.Err()
		case <-tick.C:
		}
	}
}

// publish fetches the output and publishes it through PublishVersion's
// path, with its checks. The site is looked up again: it may have been
// deleted, or the actor's access changed, while the build ran.
func (r *buildRun) publish(ctx context.Context) (BuildResult, error) {
	out, err := r.s.builds.Output(ctx, r.spec.ID)
	if err != nil {
		if ctx.Err() != nil {
			return r.end(RunCancelled, "", "cancelled", errorf(Conflict, "the build was cancelled"))
		}
		return r.fail(build.ReasonOutput, "the output could not be fetched from the build service")
	}
	defer out.Close()
	p, err := r.s.publishVersion(ctx, r.a, r.spec.Site, &limitedOutput{r: out, left: maxOutputStream}, r.req.Live, "site.build-publish", "site.build-publish-not-live")
	if err != nil && KindOf(err) != NotSynced {
		switch {
		case ctx.Err() != nil:
			return r.end(RunCancelled, "", "cancelled", errorf(Conflict, "the build was cancelled"))
		case errors.Is(err, deploy.ErrBadArchive), errors.Is(err, errOutputTooLarge):
			return r.fail(build.ReasonOutput, "the output cannot be published: "+err.Error())
		case KindOf(err) == NotFound || KindOf(err) == Forbidden:
			res, _ := r.fail(build.ReasonOutput, "the site can no longer be published to")
			return res, err
		}
		slog.Error("publishing a build failed", "site", r.spec.Site, "build", r.spec.ID, "err", err)
		return r.fail(build.ReasonInternal, "the output could not be published")
	}
	r.update.Release, r.update.Live = p.Release, p.Live
	res, _ := r.end(RunSucceeded, "", "", nil)
	res.Publication = &p
	// A NotSynced error: the version exists, the web server catches up.
	return res, err
}

// maxOutputStream bounds the output archive as it arrives, before deploy's
// own limits on what it unpacks to. A variable so tests can lower it.
var maxOutputStream = build.MaxOutputBytes + build.MaxOutputFiles*4<<10

var errOutputTooLarge = errors.New("the output is larger than allowed")

// limitedOutput fails the stream once it is longer than left.
type limitedOutput struct {
	r    io.Reader
	left int64
}

func (l *limitedOutput) Read(p []byte) (int, error) {
	if l.left <= 0 {
		var one [1]byte
		if n, _ := l.r.Read(one[:]); n > 0 {
			return 0, errOutputTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

// withLog adds the end of the build's log to a result. The service blanks
// out the values of secrets in it; the panel blanks them out again and
// leaves the log out when a secret is too short to be blanked out.
func (r *buildRun) withLog(res BuildResult, err error) (BuildResult, error) {
	for _, v := range r.spec.Secrets {
		if len(v) < build.MinSecretMask {
			return res, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, gerr := r.s.builds.Get(ctx, r.spec.ID)
	if gerr != nil {
		return res, err
	}
	off := max(b.LogBytes-buildLogTail, 0)
	lr, lerr := r.s.builds.Log(ctx, r.spec.ID, off, false)
	if lerr != nil {
		return res, err
	}
	defer lr.Close()
	raw, _ := io.ReadAll(io.LimitReader(lr, buildLogTail))
	text := strings.ToValidUTF8(string(raw), "?")
	// Longest first, so a secret that contains another is blanked whole.
	secrets := make([]string, 0, len(r.spec.Secrets))
	for _, v := range r.spec.Secrets {
		secrets = append(secrets, v)
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, v := range secrets {
		text = strings.ReplaceAll(text, v, "***")
	}
	if off > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:] // drop the cut first line
		}
	}
	res.LogTail = text
	return res, err
}

// RemoveStaleBuilds cancels and removes the builds the build service still
// knows from an earlier run of the panel. Their callers are gone, so
// nobody would publish their output or remove them; the runs they belong
// to are marked by their store (#130) when it starts. It retries while the
// service does not answer (it may start after the panel), until ctx is
// done or for at most ten minutes; the service forgets finished builds by
// itself after build.ResultTTL anyway.
func (s *Service) RemoveStaleBuilds(ctx context.Context) {
	if s.builds == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	wait := time.Second
	for {
		n, err := s.removeStaleBuilds(ctx)
		if err == nil {
			if n > 0 {
				slog.Info("removed builds of an earlier run of the panel", "builds", n)
			}
			return
		}
		slog.Debug("removing old builds: the build service does not answer yet", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func (s *Service) removeStaleBuilds(ctx context.Context) (int, error) {
	list, err := s.builds.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range list {
		if b.ID > s.build.idBase {
			continue // started by this run of the panel
		}
		if err := s.builds.Remove(ctx, b.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// buildLine keeps one build per site running: of the requests that come
// while one runs, only the newest waits.
type buildLine struct {
	mu    sync.Mutex
	sites map[string]*siteLine
}

type siteLine struct {
	running *lineTicket
	waiting *lineTicket
}

type lineTicket struct {
	turn     chan struct{} // closed when it may run
	replaced chan struct{} // closed when a newer request replaced it
	hasTurn  bool
}

var errReplaced = errors.New("replaced by a newer build")

// enter puts a request in the site's line: it runs at once if nothing of
// the site runs, else it waits, replacing the one that waited.
func (l *buildLine) enter(site string) *lineTicket {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := &lineTicket{turn: make(chan struct{}), replaced: make(chan struct{})}
	sl := l.sites[site]
	if sl == nil {
		sl = &siteLine{}
		l.sites[site] = sl
	}
	if sl.running == nil {
		sl.running, t.hasTurn = t, true
		close(t.turn)
		return t
	}
	if sl.waiting != nil {
		close(sl.waiting.replaced)
	}
	sl.waiting = t
	return t
}

// wait returns when t may run, errReplaced when a newer request replaced
// it, or ctx's error.
func (l *buildLine) wait(ctx context.Context, t *lineTicket) error {
	select {
	case <-t.turn:
		return nil
	default:
	}
	select {
	case <-t.turn:
		return nil
	case <-t.replaced:
		return errReplaced
	case <-ctx.Done():
		return ctx.Err()
	}
}

// leave takes t out of the line; if it had the turn, the waiting request
// gets it.
func (l *buildLine) leave(site string, t *lineTicket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sl := l.sites[site]
	if sl == nil {
		return
	}
	switch {
	case sl.running == t:
		sl.running = nil
		if w := sl.waiting; w != nil {
			sl.waiting = nil
			sl.running, w.hasTurn = w, true
			close(w.turn)
		}
	case sl.waiting == t:
		sl.waiting = nil
	}
	if sl.running == nil && sl.waiting == nil {
		delete(l.sites, site)
	}
}

// siteBuilding tells whether a build of the site runs or waits (tests).
func (l *buildLine) siteBuilding(site string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.sites[site]
	return ok
}
