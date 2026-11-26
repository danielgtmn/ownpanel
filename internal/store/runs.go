package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/danielgtmn/ablage"
)

// Everything GitHub's deliveries (#109) need from the store: the runs of a
// site, the delivery ids already handled, the repositories GitHub reported
// gone, and the few changes to Apps and installations a delivery makes.

// Statuses of a run.
const (
	RunQueued    = "queued"    // waits to be built
	RunRunning   = "running"   // being built
	RunSucceeded = "succeeded" // built and published
	RunFailed    = "failed"
	RunCancelled = "cancelled"
	RunSkipped   = "skipped" // a newer run of the site replaced it before it started
)

var finishedRuns = []string{RunSucceeded, RunFailed, RunCancelled, RunSkipped}

// RunFinished reports whether status is a final one.
func RunFinished(status string) bool {
	switch status {
	case RunSucceeded, RunFailed, RunCancelled, RunSkipped:
		return true
	}
	return false
}

// Run is one attempt to build and publish a site from its source.
type Run struct {
	ID           string
	Site         string
	Trigger      string
	Status       string
	SourceKind   string
	RepositoryID int64
	Repository   string
	Ref          string
	CommitSHA    string
	Message      string
	AuthorName   string
	AuthorLogin  string
	Pusher       string
	Detail       string
	CreatedAt    time.Time
	StartedAt    *time.Time // nil until it starts
	FinishedAt   *time.Time // nil until it ends
	// Release is the version a succeeded run published.
	Release string
	// PullRequest, Preview, HeadRepository and Fork: see siteRunRow. A run
	// with a Preview builds a version that is not live, tagged Preview.
	PullRequest    int
	Preview        string
	HeadRepository string
	Fork           bool
}

var errRunNotFound = notFoundf("run not found")

func runOf(row siteRunRow) Run {
	return Run{ID: row.ID, Site: row.SiteName, Trigger: row.Trigger, Status: row.Status, SourceKind: row.SourceKind,
		RepositoryID: row.RepositoryID, Repository: row.Repository, Ref: row.Ref, CommitSHA: row.CommitSHA,
		Message: row.Message, AuthorName: row.AuthorName, AuthorLogin: row.AuthorLogin, Pusher: row.Pusher,
		Detail: row.Detail, CreatedAt: row.CreatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, Release: row.Release,
		PullRequest: row.PullRequest, Preview: row.Preview, HeadRepository: row.HeadRepository, Fork: row.Fork}
}

func validCommitSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// newestFirst orders runs by their ids, which ablage makes ascending in
// the order the runs were recorded.
var newestFirst = []ablage.Order{ablage.Desc("id")}

// skipQueued marks the queued runs of a site with the same preview (""
// for runs that make a version live) skipped, except the run keep. Runs of
// a pull request never replace the site's other runs, nor runs of other
// pull requests.
func skipQueued(ctx context.Context, r repos, site, preview, keep, detail string, now time.Time) error {
	where := ablage.And(ablage.Eq("site_name", site), ablage.Eq("status", RunQueued), ablage.Eq("preview", preview))
	if keep != "" {
		where = ablage.And(where, ablage.Ne("id", keep))
	}
	_, err := r.runs.UpdateWhere(ctx, where, ablage.Row{"status": RunSkipped, "detail": detail, "finished_at": now})
	return err
}

// AddRun records a queued run of a site. In the same transaction it marks
// the site's older queued runs with the same Preview skipped (supersededDetail says why) and
// removes finished runs older than the newest keep of the site; queued and
// running runs are never removed.
func (s *Store) AddRun(ctx context.Context, r Run, supersededDetail string, keep int) (Run, error) {
	if r.Site == "" || r.Trigger == "" || r.SourceKind == "" || r.Ref == "" {
		return Run{}, invalidf("a run needs a site, a trigger, a source and a ref")
	}
	if !validCommitSHA(r.CommitSHA) {
		return Run{}, invalidf("%q is not a full commit id", r.CommitSHA)
	}
	if (r.Preview == "") != (r.PullRequest == 0) || r.PullRequest < 0 {
		return Run{}, invalidf("a run of a pull request needs its number and a preview, and only it has them")
	}
	keep = max(keep, 1)
	now := s.now()
	row := siteRunRow{SiteName: r.Site, Trigger: r.Trigger, Status: RunQueued, SourceKind: r.SourceKind,
		RepositoryID: r.RepositoryID, Repository: r.Repository, Ref: r.Ref, CommitSHA: r.CommitSHA, Message: r.Message,
		AuthorName: r.AuthorName, AuthorLogin: r.AuthorLogin, Pusher: r.Pusher, CreatedAt: now,
		PullRequest: r.PullRequest, Preview: r.Preview, HeadRepository: r.HeadRepository, Fork: r.Fork}
	err := s.tx(ctx, func(tx repos) error {
		if ok, err := tx.sites.Exists(ctx, r.Site); err != nil {
			return err
		} else if !ok {
			return errSiteNotFound
		}
		if err := skipQueued(ctx, tx, r.Site, r.Preview, "", supersededDetail, now); err != nil {
			return err
		}
		if err := tx.runs.Create(ctx, &row); err != nil {
			return err
		}
		// The oldest run that is kept; finished runs before it go.
		kept, err := tx.runs.Find(ctx, ablage.Query{Where: ablage.Eq("site_name", r.Site), OrderBy: newestFirst,
			Offset: keep - 1, Limit: 1})
		if err != nil || len(kept) == 0 {
			return err
		}
		_, err = tx.runs.DeleteWhere(ctx, ablage.And(ablage.Eq("site_name", r.Site), ablage.Lt("id", kept[0].ID),
			ablage.In("status", finishedRuns...)))
		return err
	})
	if err != nil {
		return Run{}, err
	}
	return runOf(row), nil
}

// Run returns one run.
func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	if id == "" {
		return Run{}, errRunNotFound
	}
	row, err := s.r.runs.Get(ctx, id)
	if err != nil {
		return Run{}, missing(err, errRunNotFound)
	}
	return runOf(row), nil
}

// SiteRuns lists the newest runs of a site, newest first.
func (s *Store) SiteRuns(ctx context.Context, site string, limit int) ([]Run, error) {
	rows, err := s.r.runs.Find(ctx, ablage.Query{Where: ablage.Eq("site_name", site), OrderBy: newestFirst, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Run, len(rows))
	for i, row := range rows {
		out[i] = runOf(row)
	}
	return out, nil
}

// SitesWithQueuedRuns lists the sites that have a queued run, by name.
func (s *Store) SitesWithQueuedRuns(ctx context.Context) ([]string, error) {
	groups, err := s.r.runs.Aggregate(ctx, ablage.Aggregation{Where: ablage.Eq("status", RunQueued), GroupBy: []string{"site_name"}})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		name, _ := g["site_name"].(string)
		out = append(out, name)
	}
	return out, nil
}

// ClaimNextRun marks a queued run of a site running and returns it: the
// newest of the runs that make a version live first, else the oldest
// queued run of a pull request. ErrNotFound: nothing is queued.
// ErrConflict: a run of the site is already running. Older queued runs of
// the same kind (there are none while AddRun supersedes them) are marked
// skipped.
func (s *Store) ClaimNextRun(ctx context.Context, site, supersededDetail string) (Run, error) {
	var id string
	err := s.tx(ctx, func(r repos) error {
		running, err := r.runs.Count(ctx, ablage.And(ablage.Eq("site_name", site), ablage.Eq("status", RunRunning)))
		if err != nil {
			return err
		}
		if running > 0 {
			return conflictf("a run of the site is already running")
		}
		queued := ablage.And(ablage.Eq("site_name", site), ablage.Eq("status", RunQueued))
		next, err := r.runs.First(ctx, ablage.Query{Where: ablage.And(queued, ablage.Eq("preview", "")), OrderBy: newestFirst})
		if errors.Is(err, ablage.ErrNotFound) {
			next, err = r.runs.First(ctx, ablage.Query{Where: queued, OrderBy: []ablage.Order{ablage.Asc("id")}})
		}
		if errors.Is(err, ablage.ErrNotFound) {
			return notFoundf("no queued run")
		}
		if err != nil {
			return err
		}
		id = next.ID
		now := s.now()
		if err := skipQueued(ctx, r, site, next.Preview, id, supersededDetail, now); err != nil {
			return err
		}
		return r.runs.Patch(ctx, id, ablage.Row{"status": RunRunning, "started_at": now})
	})
	if err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

// PreviewRuns lists the queued and running runs with a preview tag, of
// every site, oldest first.
func (s *Store) PreviewRuns(ctx context.Context, preview string) ([]Run, error) {
	rows, err := s.r.runs.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.Eq("preview", preview), ablage.In("status", RunQueued, RunRunning)),
		OrderBy: []ablage.Order{ablage.Asc("id")},
	})
	if err != nil {
		return nil, err
	}
	out := make([]Run, len(rows))
	for i, row := range rows {
		out[i] = runOf(row)
	}
	return out, nil
}

// FinishRun gives a queued or running run its final status. ErrConflict if
// it already has one, ErrInvalid for a status that is not final.
func (s *Store) FinishRun(ctx context.Context, id, status, detail string) (Run, error) {
	return s.FinishRunWithRelease(ctx, id, status, detail, "")
}

// FinishRunWithRelease is FinishRun that also records the version a
// succeeded run published.
func (s *Store) FinishRunWithRelease(ctx context.Context, id, status, detail, release string) (Run, error) {
	if !RunFinished(status) {
		return Run{}, invalidf("%q is not a final status of a run", status)
	}
	if id == "" {
		return Run{}, errRunNotFound
	}
	err := s.tx(ctx, func(r repos) error {
		cur, err := r.runs.Get(ctx, id)
		if err != nil {
			return missing(err, errRunNotFound)
		}
		if RunFinished(cur.Status) {
			return conflictf("the run has already ended (%s)", cur.Status)
		}
		return r.runs.Patch(ctx, id, ablage.Row{"status": status, "detail": detail, "finished_at": s.now(), "release": release})
	})
	if err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

// FailRunningRuns marks every running run failed with detail and returns
// how many there were. For the start of the panel: nothing builds them
// any more.
func (s *Store) FailRunningRuns(ctx context.Context, detail string) (int, error) {
	n, err := s.r.runs.UpdateWhere(ctx, ablage.Eq("status", RunRunning),
		ablage.Row{"status": RunFailed, "detail": detail, "finished_at": s.now()})
	return int(n), err
}

// ---- logs of runs ----

// PutRunLog stores the log of a run, replacing the one it had.
func (s *Store) PutRunLog(ctx context.Context, runID, log string, truncated bool) error {
	return s.tx(ctx, func(r repos) error {
		if _, err := r.runLogs.DeleteWhere(ctx, ablage.Eq("run_id", runID)); err != nil {
			return err
		}
		err := r.runLogs.Create(ctx, &siteRunLogRow{RunID: runID, Log: log, Truncated: truncated, CreatedAt: s.now()})
		if errors.Is(err, ablage.ErrReference) {
			return errRunNotFound
		}
		return err
	})
}

// RunLog returns the stored log of a run; ErrNotFound if it has none.
func (s *Store) RunLog(ctx context.Context, runID string) (log string, truncated bool, err error) {
	row, err := s.r.runLogs.First(ctx, ablage.Query{Where: ablage.Eq("run_id", runID)})
	if err != nil {
		return "", false, missing(err, notFoundf("the run has no log"))
	}
	return row.Log, row.Truncated, nil
}

// ---- delivery ids ----

// ClaimDelivery records a GitHub delivery id before it is handled; appID
// is the panel's id of the App that signed it. False: the id was seen
// before (and not released), so the delivery is a repeat.
func (s *Store) ClaimDelivery(ctx context.Context, id, appID string) (bool, error) {
	if id == "" {
		return false, invalidf("a delivery needs an id")
	}
	err := s.r.deliveries.Create(ctx, &githubDeliveryRow{ID: id, AppID: appID, ReceivedAt: s.now()})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ablage.ErrDuplicate):
		return false, nil
	case errors.Is(err, ablage.ErrReference):
		return false, errGitHubAppNotFound
	}
	return false, err
}

// ReleaseDelivery forgets a claimed delivery id whose handling failed, so
// the same delivery is handled when it is sent again.
func (s *Store) ReleaseDelivery(ctx context.Context, id string) error {
	_, err := s.r.deliveries.DeleteWhere(ctx, ablage.Eq("id", id))
	return err
}

// ForgetDeliveries removes delivery ids received before t, and the oldest
// beyond max (up to 10000 of those per call). It returns how many it
// removed.
func (s *Store) ForgetDeliveries(ctx context.Context, before time.Time, max int) (int, error) {
	removed := 0
	err := s.tx(ctx, func(r repos) error {
		extra, err := r.deliveries.Find(ctx, ablage.Query{
			OrderBy: []ablage.Order{ablage.Desc("received_at"), ablage.Desc("id")},
			Offset:  max, Limit: 10000,
		})
		if err != nil {
			return err
		}
		ids := make([]string, len(extra))
		for i, d := range extra {
			ids[i] = d.ID
		}
		n, err := r.deliveries.DeleteWhere(ctx, ablage.Or(ablage.Lt("received_at", seconds(before)), ablage.In("id", ids...)))
		removed = int(n)
		return err
	})
	return removed, err
}

// ---- repositories GitHub reported gone ----

func goneID(installationID, repositoryID int64) string {
	return fmt.Sprintf("%d/%d", installationID, repositoryID)
}

// SetRepositoryGone records whether GitHub reported that an installation
// no longer sees a repository. For an installation the panel does not know
// (any more) there is nothing to record.
func (s *Store) SetRepositoryGone(ctx context.Context, installationID, repositoryID int64, gone bool) error {
	id := goneID(installationID, repositoryID)
	if !gone {
		_, err := s.r.reposGone.DeleteWhere(ctx, ablage.Eq("id", id))
		return err
	}
	err := s.r.reposGone.Create(ctx, &githubRepositoryGoneRow{ID: id, InstallationID: installationKey(installationID),
		RepositoryID: repositoryID, Since: s.now()})
	if errors.Is(err, ablage.ErrDuplicate) || errors.Is(err, ablage.ErrReference) {
		return nil
	}
	return err
}

// RepositoryGone reports whether GitHub reported that an installation no
// longer sees a repository.
func (s *Store) RepositoryGone(ctx context.Context, installationID, repositoryID int64) (bool, error) {
	return s.r.reposGone.Exists(ctx, goneID(installationID, repositoryID))
}

// ---- Apps and installations, as deliveries change them ----

// GitHubAppByAppID returns the App with GitHub's id appID.
func (s *Store) GitHubAppByAppID(ctx context.Context, appID int64) (GitHubApp, error) {
	row, err := s.r.githubApps.First(ctx, ablage.Query{Where: ablage.Eq("app_id", appID)})
	if err != nil {
		return GitHubApp{}, missing(err, errGitHubAppNotFound)
	}
	list, err := githubAppsOf(ctx, s.r, []githubAppRow{row})
	if err != nil {
		return GitHubApp{}, err
	}
	return list[0], nil
}

// installationWhere matches the installation id of the App appID (the
// panel's id).
func installationWhere(appID string, id int64) ablage.Cond {
	return ablage.And(ablage.Eq("id", installationKey(id)), ablage.Eq("app_id", appID))
}

// DeleteGitHubInstallation forgets an installation of an App (the panel's
// id) and, with it, the repositories reported gone for it; sources linked
// through it show "connection missing". ErrNotFound if the App has no such
// installation.
func (s *Store) DeleteGitHubInstallation(ctx context.Context, appID string, id int64) error {
	n, err := s.r.githubInsts.DeleteWhere(ctx, installationWhere(appID, id))
	if err != nil {
		return err
	}
	if n == 0 {
		return errGitHubInstallationNotFound
	}
	return nil
}

// SetGitHubInstallationSuspended records whether an installation of an App
// (the panel's id) is suspended. ErrNotFound if the App has no such
// installation.
func (s *Store) SetGitHubInstallationSuspended(ctx context.Context, appID string, id int64, suspended bool) error {
	n, err := s.r.githubInsts.UpdateWhere(ctx, installationWhere(appID, id), ablage.Row{"suspended": suspended, "updated_at": s.now()})
	if err != nil {
		return err
	}
	if n == 0 {
		return errGitHubInstallationNotFound
	}
	return nil
}
