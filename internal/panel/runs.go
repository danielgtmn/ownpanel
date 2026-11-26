package panel

import (
	"context"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtmn/ownpanel/internal/store"
)

// Runs: every push to the linked branch of a site with "publish on push"
// on records a run (github_webhook.go), and so does "Build now"
// (runner.go). A run is the unit the build on the server works on: the
// runner takes queued runs and builds them (runner.go); on a panel without
// a build service, runs stay queued and say so.
//
// Contract for the builder (#113), which uses only these functions:
//
//   - SitesWithQueuedRuns lists the sites with work waiting (also after a
//     restart: runs are stored).
//   - ClaimNextRun(site) marks the site's newest queued run running and
//     returns it; older queued runs become skipped. It returns Conflict
//     while another run of the site is running (one build per site at a
//     time) and NotFound when nothing is queued.
//   - FinishRun(id, status, detail) ends a queued or running run with
//     succeeded, failed or cancelled (detail: a short text for people,
//     never a secret or a log). A run ends exactly once (Conflict after).
//   - A new push while a run is queued marks that run skipped ("replaced
//     by a newer push"); a running run is left alone, the new one waits.
//   - The run's commit is CommitSHA (40 hex, checked), fetched from
//     RepositoryID through the source's installation as it is at build
//     time; the source may have changed or gone since, so the builder
//     reads it again and fails the run if it no longer matches.
//
// Who may see runs and their logs: whoever may see the site. Starting and
// cancelling them (runner.go): the same as changing the site.

// RunsKept is how many runs of a site are kept; older finished runs are
// removed when a new one is recorded. Queued and running runs are always
// kept.
const RunsKept = 30

// Run triggers.
const (
	RunTriggerPush = "push"
	// RunTriggerManual: someone asked for a build ("Build now"); Pusher
	// names them.
	RunTriggerManual = "manual"
)

const (
	runSupersededDetail = "replaced by a newer push"
	// Lengths of the texts of a run, in characters.
	runMessageMax = 200
	runNameMax    = 100
)

// Run is one attempt to build and publish a site from its source. Message,
// author and pusher come from GitHub and are plain text: show them as
// text, never as markup.
type Run struct {
	ID      string `json:"id"`
	Site    string `json:"site"`
	Trigger string `json:"trigger"`
	// Status: queued, running, succeeded, failed, cancelled or skipped.
	Status       string `json:"status"`
	Repository   string `json:"repository"`
	RepositoryID int64  `json:"-"`
	Ref          string `json:"ref"`
	Branch       string `json:"branch,omitempty"`
	Commit       string `json:"commit"`
	ShortCommit  string `json:"short_commit"`
	// CommitURL is the commit's page on GitHub, "" if the repository's
	// name is not a plain owner/name.
	CommitURL   string `json:"commit_url,omitempty"`
	Message     string `json:"message"`
	AuthorName  string `json:"author_name"`
	AuthorLogin string `json:"author_login,omitempty"`
	Pusher      string `json:"pusher,omitempty"`
	// Detail says why a run was skipped, failed or cancelled.
	Detail string `json:"detail,omitempty"`
	// Waiting is set on a queued run while the panel cannot build yet.
	Waiting    string     `json:"waiting,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Release is the version a succeeded run published.
	Release string `json:"release,omitempty"`
}

// SiteRuns are the newest runs of a site, newest first.
type SiteRuns struct {
	Site string `json:"site"`
	Runs []Run  `json:"runs"`
	// Kept is how many runs the panel keeps per site.
	Kept                int  `json:"kept"`
	PublishingAvailable bool `json:"publishing_available"`
}

// runWaiting is what a queued run says while there are no builds.
const runWaiting = "waits for building on the server, which this panel cannot do yet"

// viewRun is runViewFor with what depends on the panel: whether a queued
// run waits for building to be set up.
func (s *Service) viewRun(r store.Run) Run {
	v := s.runViewFor(r)
	if r.Status == store.RunQueued && !s.PublishingAvailable() {
		v.Waiting = runWaiting
	}
	return v
}

func runView(r store.Run) Run {
	v := Run{
		ID: r.ID, Site: r.Site, Trigger: r.Trigger, Status: r.Status, Repository: r.Repository,
		RepositoryID: r.RepositoryID, Ref: r.Ref, Commit: r.CommitSHA, Message: r.Message, AuthorName: r.AuthorName,
		AuthorLogin: r.AuthorLogin, Pusher: r.Pusher, Detail: r.Detail, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt,
		FinishedAt: r.FinishedAt, Release: r.Release,
	}
	v.Branch, _ = strings.CutPrefix(r.Ref, "refs/heads/")
	if v.Branch == r.Ref {
		v.Branch = ""
	}
	v.ShortCommit = r.CommitSHA
	if len(v.ShortCommit) > 7 {
		v.ShortCommit = v.ShortCommit[:7]
	}
	return v
}

// runViewFor is runView with the commit's page on GitHub.
func (s *Service) runViewFor(r store.Run) Run {
	v := runView(r)
	owner, name, ok := strings.Cut(r.Repository, "/")
	if ok && owner != "" && name != "" && !strings.Contains(name, "/") && v.Commit != "" {
		v.CommitURL = s.github.WebURL() + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/commit/" + url.PathEscape(v.Commit)
	}
	return v
}

// ListSiteRuns returns the runs of a site the actor may see.
func (s *Service) ListSiteRuns(ctx context.Context, a Actor, site string) (SiteRuns, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return SiteRuns{}, err
	}
	list, err := s.store.SiteRuns(ctx, st.Name, RunsKept)
	if err != nil {
		return SiteRuns{}, storeError(err)
	}
	out := SiteRuns{Site: st.Name, Runs: make([]Run, 0, len(list)), Kept: RunsKept, PublishingAvailable: s.PublishingAvailable()}
	for _, r := range list {
		out.Runs = append(out.Runs, s.viewRun(r))
	}
	return out, nil
}

// lastRun is the newest run of a site, nil if it has none.
func (s *Service) lastRun(ctx context.Context, site string) (*Run, error) {
	list, err := s.store.SiteRuns(ctx, site, 1)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	v := s.viewRun(list[0])
	return &v, nil
}

// ---- for the builder (runner.go); not reachable through the API or MCP ----

// SitesWithQueuedRuns lists the sites that have a queued run.
func (s *Service) SitesWithQueuedRuns(ctx context.Context) ([]string, error) {
	out, err := s.store.SitesWithQueuedRuns(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	return out, nil
}

// ClaimNextRun takes the newest queued run of a site to build it (see the
// contract above).
func (s *Service) ClaimNextRun(ctx context.Context, site string) (Run, error) {
	r, err := s.store.ClaimNextRun(ctx, site, runSupersededDetail)
	if err != nil {
		return Run{}, storeError(err)
	}
	return s.viewRun(r), nil
}

// FinishRun ends a run with succeeded, failed or cancelled.
func (s *Service) FinishRun(ctx context.Context, id string, status, detail string) (Run, error) {
	switch status {
	case store.RunSucceeded, store.RunFailed, store.RunCancelled:
	default:
		return Run{}, errorf(Invalid, "a run cannot end as %q", status)
	}
	r, err := s.store.FinishRun(ctx, id, status, plainText(detail, runMessageMax))
	if err != nil {
		return Run{}, storeError(err)
	}
	return s.viewRun(r), nil
}

// plainText makes untrusted text fit for a one-line display: its first
// line, valid UTF-8, without control or formatting characters (which
// could reorder what is shown), trimmed and at most max characters.
func plainText(s string, max int) string {
	s = strings.ToValidUTF8(s, "�")
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || !unicode.IsPrint(r) && r != ' ' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}
