package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtmn/ownpanel/internal/deploy"
	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// Previews of pull requests (#116). A pull request against the linked
// branch of a site with "previews for pull requests" on records a run, like
// a push does (github_webhook.go), but the runner builds it as a version
// that is not live, tagged with the pull request (deploy.AddTagged), with
// its own preview address. Of each pull request only the newest preview is
// kept; tagged versions do not count against the site's normal versions
// and have their own cap (deploy.KeepTagged), so many pull requests can
// never push out the live version or the one to go back to.
//
//   - Runs of a pull request only replace queued runs of the same pull
//     request, never the site's other runs (store.AddRun).
//   - Closing (or merging) a pull request cancels its runs and removes its
//     previews from every site. A version made live is no preview any more
//     and stays.
//   - Pull requests from forks are untrusted: they are built only when the
//     site has "pull requests from forks" on as well, and then without the
//     site's variables (neither plain nor secret) and with an empty cache of
//     their own (build.Spec.Untrusted), so what they leave behind never
//     reaches a later build of the site.
//   - GitHub hears about each run through a commit status ("ownpanel
//     preview") and one comment of the panel's own on the pull request,
//     found again by a hidden marker and by its author (the App's bot), and
//     updated on every push and when the pull request is closed.
//
// The preview link carries the key that opens the preview. Comments and
// statuses of a public repository are public, so the panel puts the link
// with its key only on pull requests of private repositories (asked of
// GitHub when it posts, not taken from the delivery). For a public
// repository it links to the run in the panel instead, where whoever may
// see the site finds the preview's link. A repository that is made public
// loses its pull requests' previews (handleRepository), so links posted
// while it was private stop working.

const (
	// RunTriggerPullRequest: a pull request was opened, reopened or got a
	// new commit.
	RunTriggerPullRequest = "pull_request"
	// previewStatusContext names the panel's status of a preview on GitHub.
	previewStatusContext = "ownpanel preview"
	// runPullRequestClosed is the detail of runs cancelled by closing.
	runPullRequestClosed = "the pull request was closed"
	// previewCleanupTime bounds telling GitHub about a closed pull request.
	previewCleanupTime = 2 * time.Minute
)

// PullRequestPreviewsKept is how many pull requests of a site have a
// preview at a time; the previews of the pull requests built longest ago go
// first.
const PullRequestPreviewsKept = deploy.KeepTagged

// previewTag tags the versions of a pull request of a repository.
func previewTag(repositoryID int64, number int) string {
	return fmt.Sprintf("pr-%d-%d", repositoryID, number)
}

// parsePreviewTag reads previewTag.
func parsePreviewTag(tag string) (repositoryID int64, number int, ok bool) {
	rest, ok := strings.CutPrefix(tag, "pr-")
	if !ok {
		return 0, 0, false
	}
	r, n, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, 0, false
	}
	repo, err1 := strconv.ParseInt(r, 10, 64)
	num, err2 := strconv.Atoi(n)
	if err1 != nil || err2 != nil || repo <= 0 || num <= 0 {
		return 0, 0, false
	}
	return repo, num, true
}

// ---- the delivery ----

type hookPullRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type hookPullRequest struct {
	hookCommon
	Number      int `json:"number"`
	PullRequest *struct {
		Number    int    `json:"number"`
		State     string `json:"state"`
		Merged    bool   `json:"merged"`
		Title     string `json:"title"`
		UpdatedAt string `json:"updated_at"`
		User      *struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref  string        `json:"ref"`
			SHA  string        `json:"sha"`
			Repo *hookPullRepo `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string        `json:"ref"`
			Repo *hookPullRepo `json:"repo"`
		} `json:"base"`
	} `json:"pull_request"`
	Sender *struct {
		Login string `json:"login"`
	} `json:"sender"`
}

func (s *Service) handlePullRequest(ctx context.Context, app store.GitHubApp, p hookPullRequest) (GitHubDeliveryResult, error) {
	ignored := GitHubDeliveryResult{Result: DeliveryIgnored}
	pr := p.PullRequest
	if p.Repository == nil || p.Repository.ID <= 0 || pr == nil || p.Number <= 0 || pr.Number != p.Number ||
		pr.Base.Repo == nil || pr.Base.Repo.ID != p.Repository.ID {
		return GitHubDeliveryResult{}, errMalformed
	}
	inst, ours, err := s.deliveryInstallation(ctx, app, p.hookCommon)
	if err != nil || !ours {
		return ignored, err
	}
	if github.ValidFullName(p.Repository.FullName) {
		if err := s.store.RenameSourceRepository(ctx, p.Repository.ID, p.Repository.FullName); err != nil {
			return GitHubDeliveryResult{}, storeError(err)
		}
	}
	switch p.Action {
	case "closed":
		return s.closePullRequest(ctx, inst.ID, p.Repository.ID, p.Number)
	case "opened", "synchronize", "reopened":
	default:
		// edited, labeled, ready_for_review, …: nothing to build.
		return ignored, nil
	}
	if inst.Suspended {
		return ignored, nil
	}
	if gone, err := s.store.RepositoryGone(ctx, inst.ID, p.Repository.ID); err != nil || gone {
		return ignored, storeErrorOrNil(err)
	}
	if !github.ValidCommitSHA(pr.Head.SHA) {
		return GitHubDeliveryResult{}, errMalformed
	}
	updated, err := time.Parse(time.RFC3339, pr.UpdatedAt)
	if now := s.now(); err != nil || updated.Before(now.Add(-GitHubDeliveryMemory)) || updated.After(now.Add(24*time.Hour)) {
		slog.Warn("GitHub pull request ignored: too old or without a time", "connection", app.ID, "repository", p.Repository.ID)
		return ignored, nil
	}
	branch := pr.Base.Ref
	if !github.ValidBranch(branch) {
		return ignored, nil
	}
	// A deleted fork has no repository any more: it is still a fork.
	fork := pr.Head.Repo == nil || pr.Head.Repo.ID != p.Repository.ID
	run := store.Run{
		Trigger: RunTriggerPullRequest, SourceKind: store.SourceGitHub, RepositoryID: p.Repository.ID,
		Ref: "refs/pull/" + strconv.Itoa(p.Number) + "/head", CommitSHA: pr.Head.SHA, Message: plainText(pr.Title, runMessageMax),
		PullRequest: p.Number, Preview: previewTag(p.Repository.ID, p.Number), Fork: fork,
	}
	if pr.User != nil && github.ValidLogin(pr.User.Login) {
		run.AuthorLogin, run.AuthorName = pr.User.Login, pr.User.Login
	}
	if p.Sender != nil && github.ValidLogin(p.Sender.Login) {
		run.Pusher = p.Sender.Login
	}
	if fork && pr.Head.Repo != nil && github.ValidFullName(pr.Head.Repo.FullName) {
		run.HeadRepository = pr.Head.Repo.FullName
	}
	sources, err := s.store.SiteSourcesOfRepository(ctx, p.Repository.ID)
	if err != nil {
		return GitHubDeliveryResult{}, storeError(err)
	}
	out := ignored
	for _, src := range sources {
		if src.InstallationID != inst.ID || src.Branch != branch || !src.PullRequestPreviews || fork && !src.ForkPreviews {
			continue
		}
		r := run
		r.Site, r.Repository = src.Site, src.Repository
		if _, err := s.store.AddRun(ctx, r, runSupersededDetail, RunsKept); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return GitHubDeliveryResult{}, storeError(err)
		}
		out.Result = DeliveryHandled
		out.Runs++
	}
	return out, nil
}

// closePullRequest cancels the runs of a closed pull request and removes
// its previews from every site. The web server and GitHub are told
// afterwards, outside the delivery.
func (s *Service) closePullRequest(ctx context.Context, installationID, repositoryID int64, number int) (GitHubDeliveryResult, error) {
	tag := previewTag(repositoryID, number)
	out := GitHubDeliveryResult{Result: DeliveryIgnored}
	runs, err := s.store.PreviewRuns(ctx, tag)
	if err != nil {
		return GitHubDeliveryResult{}, storeError(err)
	}
	affected := map[string]bool{}
	for _, r := range runs {
		affected[r.Site] = true
		out.Result = DeliveryHandled
		s.cancelRun(ctx, r, runPullRequestClosed)
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return GitHubDeliveryResult{}, storeError(err)
	}
	removed := false
	for _, site := range sites {
		ids, err := s.deploys.RemoveTagged(site.Name, tag)
		if err != nil {
			slog.Error("could not remove the preview of a closed pull request", "site", site.Name, "err", err)
		}
		if len(ids) > 0 {
			removed, affected[site.Name] = true, true
			out.Result = DeliveryHandled
			s.record(ctx, System, "site.preview-remove", site.Name, fmt.Sprintf("pull request #%d closed: %s", number, strings.Join(ids, ", ")))
		}
	}
	// Sites with previews on may have a comment from an earlier build.
	sources, err := s.store.SiteSourcesOfRepository(ctx, repositoryID)
	if err != nil {
		return GitHubDeliveryResult{}, storeError(err)
	}
	for _, src := range sources {
		if src.InstallationID == installationID && src.PullRequestPreviews {
			affected[src.Site] = true
		}
	}
	s.runner.bg.Add(1)
	go func() {
		defer s.runner.bg.Done()
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), previewCleanupTime)
		defer cancel()
		if removed {
			if err := s.syncSaved(bg); err != nil {
				slog.Warn("the web server still serves the preview of a closed pull request until the next update", "err", err)
			}
		}
		for site := range affected {
			s.closedPreviewComment(bg, site, repositoryID, number)
		}
	}()
	return out, nil
}

// cancelRun ends a queued run, or stops the build of a running one, with
// detail as the reason.
func (s *Service) cancelRun(ctx context.Context, r store.Run, detail string) {
	if r.Status == store.RunRunning {
		s.runner.mu.Lock()
		ar := s.runner.active[r.ID]
		if ar != nil {
			ar.reason = detail
			ar.cancel()
		}
		s.runner.mu.Unlock()
		if ar != nil {
			return // the runner ends it
		}
	}
	if _, err := s.store.FinishRun(ctx, r.ID, store.RunCancelled, detail); err != nil && !errors.Is(err, store.ErrConflict) {
		slog.Error("could not cancel a run", "site", r.Site, "run", r.ID, "err", err)
	}
}

// removeRepositoryPreviews removes the previews of every pull request of a
// repository, from every site (the repository was made public: links
// posted while it was private must stop working).
func (s *Service) removeRepositoryPreviews(ctx context.Context, repositoryID int64) error {
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return storeError(err)
	}
	removed := false
	for _, site := range sites {
		for _, tag := range s.deploys.Tags(site.Name) {
			if repo, _, ok := parsePreviewTag(tag); !ok || repo != repositoryID {
				continue
			}
			ids, err := s.deploys.RemoveTagged(site.Name, tag)
			if err != nil {
				slog.Error("could not remove a preview", "site", site.Name, "err", err)
			}
			if len(ids) > 0 {
				removed = true
				s.record(ctx, System, "site.preview-remove", site.Name, "the repository was made public: "+strings.Join(ids, ", "))
			}
		}
	}
	if removed {
		s.runner.bg.Add(1)
		go func() {
			defer s.runner.bg.Done()
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), previewCleanupTime)
			defer cancel()
			_ = s.syncSaved(bg)
		}()
	}
	return nil
}

// ---- the setting ----

// SitePreviewsInput says which pull requests are built as previews.
type SitePreviewsInput struct {
	// PullRequests: pull requests from the same repository against the
	// linked branch.
	PullRequests bool `json:"pull_requests"`
	// Forks: pull requests from forks too (needs PullRequests). They are
	// built without the site's variables.
	Forks bool `json:"forks"`
}

// SetSitePreviews changes whether pull requests to a site's linked
// repository are built as previews. Who may change the site may change it.
func (s *Service) SetSitePreviews(ctx context.Context, a Actor, site string, in SitePreviewsInput) (SiteSourceState, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return SiteSourceState{}, err
	}
	if err := needWrite(a); err != nil {
		return SiteSourceState{}, err
	}
	if in.Forks && !in.PullRequests {
		return SiteSourceState{}, errorf(Invalid, "pull requests from forks are only built when pull requests are")
	}
	src, err := s.store.SiteSource(ctx, st.Name)
	if errors.Is(err, store.ErrNotFound) {
		return SiteSourceState{}, errorf(Conflict, "the site is not linked to a repository")
	}
	if err != nil {
		return SiteSourceState{}, storeError(err)
	}
	if src.Kind != store.SourceGitHub {
		return SiteSourceState{}, errorf(Conflict, "previews of pull requests need a repository on a connected GitHub account")
	}
	if _, err := s.store.SetSourcePreviews(ctx, st.Name, in.PullRequests, in.Forks); err != nil {
		return SiteSourceState{}, storeError(err)
	}
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	s.record(ctx, a, "site.previews", st.Name, fmt.Sprintf("pull requests %s, from forks %s", onOff(in.PullRequests), onOff(in.Forks)))
	return s.GetSiteSource(ctx, a, st.Name)
}

// previewRunAllowed checks, when the runner takes a run of a pull request,
// that the site still wants it built.
func (s *Service) previewRunAllowed(ctx context.Context, r store.Run) error {
	src, err := s.store.SiteSource(ctx, r.Site)
	if errors.Is(err, store.ErrNotFound) {
		return errorf(Conflict, "the site is not linked to a repository any more")
	}
	if err != nil {
		return storeError(err)
	}
	switch {
	case src.Kind != store.SourceGitHub || src.RepositoryID != r.RepositoryID:
		return errorf(Conflict, "the site is linked to another repository now")
	case !src.PullRequestPreviews:
		return errorf(Conflict, "previews of pull requests are switched off for the site")
	case r.Fork && !src.ForkPreviews:
		return errorf(Conflict, "previews of pull requests from forks are switched off for the site")
	}
	return nil
}

// ---- GitHub: the status and the comment ----

// previewMarker marks the panel's comment about a site on a pull request.
// Only a comment of the App's own bot that carries it is changed.
func previewMarker(site string) string { return "<!-- ownpanel-preview:" + site + " -->" }

// runPanelURL is the run's page in the panel ("" without a panel address).
func (s *Service) runPanelURL(r store.Run) string {
	base, err := s.githubBase()
	if err != nil {
		return ""
	}
	return base + "/#/sites/" + url.PathEscape(r.Site) + "?run=" + url.QueryEscape(r.ID)
}

// previewGitHub is what the panel needs to write to a pull request of a
// site's repository: a token that may write its statuses and pull
// requests (nothing else, that repository only), and the App.
type previewGitHub struct {
	src   store.SiteSource
	app   store.GitHubApp
	token github.Token
	owner string
	name  string
}

func (s *Service) previewGitHubFor(ctx context.Context, site string, repositoryID int64) (*previewGitHub, error) {
	src, err := s.store.SiteSource(ctx, site)
	if err != nil {
		return nil, err
	}
	if src.Kind != store.SourceGitHub || src.InstallationID == 0 || src.RepositoryID != repositoryID {
		return nil, errors.New("the site is not linked to the repository")
	}
	inst, app, err := s.installationApp(ctx, src.InstallationID)
	if err != nil {
		return nil, err
	}
	if inst.Suspended {
		return nil, errors.New("access is paused on GitHub")
	}
	auth, err := s.appAuth(app)
	if err != nil {
		return nil, err
	}
	tok, err := auth.InstallationToken(ctx, src.InstallationID, github.TokenRequest{
		RepositoryIDs: []int64{src.RepositoryID}, Permissions: map[string]string{"statuses": "write", "pull_requests": "write"},
	})
	if err != nil {
		return nil, err
	}
	owner, name := repoParts(src.Repository)
	return &previewGitHub{src: src, app: app, token: tok, owner: owner, name: name}, nil
}

// reportPreview tells GitHub how the run of a pull request ended: a
// commit status and the panel's comment on the pull request. Failures are
// logged; the run is not affected.
func (s *Service) reportPreview(ctx context.Context, r store.Run) {
	if r.Status == store.RunSkipped || r.Status == store.RunCancelled && r.Detail == runPullRequestClosed {
		s.setCommitStatus(ctx, r, github.StatusError, "Cancelled: "+r.Detail)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*githubTimeout)
	defer cancel()
	g, err := s.previewGitHubFor(ctx, r.Site, r.RepositoryID)
	if err != nil {
		slog.Info("could not report a preview to GitHub", "site", r.Site, "run", r.ID, "err", err)
		return
	}
	// Whether the repository is private decides whether the key may go on
	// GitHub. When GitHub cannot say, it is treated as public.
	pull, err := s.github.GetPullRequest(ctx, g.token, g.owner, g.name, r.PullRequest)
	private := err == nil && pull.Private
	open := err == nil && pull.State == github.PullOpen
	if err != nil {
		slog.Info("could not look at a pull request on GitHub", "site", r.Site, "run", r.ID, "err", err)
	}
	panelURL := s.runPanelURL(r)
	previewURL := ""
	if r.Status == store.RunSucceeded && s.deploys.HasRelease(r.Site, r.Release) {
		previewURL = s.previewURL(r.Site, r.Release)
	}
	st := github.CommitStatus{Context: previewStatusContext, TargetURL: panelURL}
	switch r.Status {
	case store.RunSucceeded:
		st.State, st.Description = github.StatusSuccess, "Preview ready"
		if previewURL != "" && private {
			st.TargetURL = previewURL
		} else if previewURL != "" {
			st.Description = "Preview ready; open it from the panel"
		}
	case store.RunCancelled:
		st.State, st.Description = github.StatusError, "Cancelled: "+r.Detail
	default:
		st.State, st.Description = github.StatusFailure, "Failed: "+r.Detail
	}
	st.Description = plainText(st.Description, 140)
	if err := s.github.SetCommitStatus(ctx, g.token, g.owner, g.name, r.CommitSHA, st); err != nil {
		slog.Info("could not report a preview to GitHub", "site", r.Site, "run", r.ID, "err", err)
	}
	if r.Status == store.RunCancelled {
		return // the comment keeps what it said about the last build
	}
	body := previewCommentBody(r, previewURL, panelURL, private)
	s.writePreviewComment(ctx, g, r.Site, r.PullRequest, body, open)
}

// closedPreviewComment updates the panel's comment on a closed pull
// request, if it has one.
func (s *Service) closedPreviewComment(ctx context.Context, site string, repositoryID int64, number int) {
	g, err := s.previewGitHubFor(ctx, site, repositoryID)
	if err != nil {
		return
	}
	body := previewMarker(site) + "\n**Preview of the site " + site + "**\n\nThis pull request was closed, so its preview was removed.\n"
	s.writePreviewComment(ctx, g, site, number, body, false)
}

// writePreviewComment replaces the body of the panel's comment about site
// on a pull request, or (create) writes it if there is none.
func (s *Service) writePreviewComment(ctx context.Context, g *previewGitHub, site string, number int, body string, create bool) {
	comments, err := s.github.IssueComments(ctx, g.token, g.owner, g.name, number)
	if err != nil {
		slog.Info("could not read the comments of a pull request", "site", site, "pull_request", number, "err", err)
		return
	}
	marker, bot := previewMarker(site), g.app.Slug+"[bot]"
	for _, c := range comments {
		// Anyone can write the marker: only the App's own comment counts.
		if c.UserType != "Bot" || !strings.EqualFold(c.UserLogin, bot) || c.AppID != 0 && c.AppID != g.app.AppID ||
			!strings.Contains(c.Body, marker) {
			continue
		}
		if c.Body == body {
			return
		}
		if err := s.github.UpdateIssueComment(ctx, g.token, g.owner, g.name, c.ID, body); err != nil {
			slog.Info("could not update the comment on a pull request", "site", site, "pull_request", number, "err", err)
		}
		return
	}
	if !create {
		return
	}
	if _, err := s.github.CreateIssueComment(ctx, g.token, g.owner, g.name, number, body); err != nil {
		slog.Info("could not comment on a pull request", "site", site, "pull_request", number, "err", err)
	}
}

// previewCommentBody is the panel's comment about a run of a pull request.
// It contains nothing GitHub or the pull request supplied except the
// commit id (checked: hexadecimal), and the preview's key only when the
// repository is private.
func previewCommentBody(r store.Run, previewURL, panelURL string, private bool) string {
	var b strings.Builder
	short := r.CommitSHA
	if len(short) > 7 {
		short = short[:7]
	}
	b.WriteString(previewMarker(r.Site) + "\n")
	b.WriteString("**Preview of the site " + r.Site + "**\n\n")
	link := func(text, u string) string {
		if u == "" {
			return text
		}
		return "[" + text + "](" + u + ")"
	}
	switch {
	case r.Status == store.RunSucceeded && previewURL != "" && private:
		fmt.Fprintf(&b, "Commit %s is ready to look at: %s. It is not live.\n\n", short, link("open the preview", previewURL))
		b.WriteString("Anyone with this link can open the preview until the pull request is closed. Share it only with people who should see it.\n")
	case r.Status == store.RunSucceeded && previewURL != "":
		fmt.Fprintf(&b, "Commit %s is ready to look at. It is not live.\n\n", short)
		fmt.Fprintf(&b, "Because this repository is public, its link is only shown in the panel, to people who may see the site: %s.\n",
			link("open the build in the panel", panelURL))
	case r.Status == store.RunSucceeded:
		fmt.Fprintf(&b, "Commit %s was built as a version that is not live. Previews have no address on this panel yet: %s.\n",
			short, link("see the build in the panel", panelURL))
	default:
		fmt.Fprintf(&b, "Commit %s could not be built: %s.\n", short, link("see why in the panel", panelURL))
	}
	if r.Fork {
		b.WriteString("\nThis pull request comes from a fork: it was built without the site's variables and secrets.\n")
	}
	b.WriteString("\nThis comment is updated with every push to the pull request; the preview is removed when it is closed.\n")
	return b.String()
}
