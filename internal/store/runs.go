package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Everything GitHub's deliveries (#109) need from the store: the runs of a
// site, the delivery ids already handled, the repositories GitHub reported
// gone, and the few changes to Apps and installations a delivery makes.
// Plain rows and plain queries on purpose, without joins.

// Statuses of a run.
const (
	RunQueued    = "queued"    // waits to be built
	RunRunning   = "running"   // being built
	RunSucceeded = "succeeded" // built and published
	RunFailed    = "failed"
	RunCancelled = "cancelled"
	RunSkipped   = "skipped" // a newer run of the site replaced it before it started
)

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
	ID           int64
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
}

const runCols = `id, site_name, trigger, status, source_kind, repository_id, repository, ref, commit_sha,
	message, author_name, author_login, pusher, detail, created_at, started_at, finished_at`

func runTime(sec int64) *time.Time {
	if sec == 0 {
		return nil
	}
	t := unix(sec)
	return &t
}

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var created, started, finished int64
	err := row.Scan(&r.ID, &r.Site, &r.Trigger, &r.Status, &r.SourceKind, &r.RepositoryID, &r.Repository, &r.Ref,
		&r.CommitSHA, &r.Message, &r.AuthorName, &r.AuthorLogin, &r.Pusher, &r.Detail, &created, &started, &finished)
	if err != nil {
		if noRows(err) {
			return Run{}, notFoundf("run not found")
		}
		return Run{}, err
	}
	r.CreatedAt, r.StartedAt, r.FinishedAt = unix(created), runTime(started), runTime(finished)
	return r, nil
}

func scanRuns(rows *sql.Rows, err error) ([]Run, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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

// AddRun records a queued run of a site. In the same transaction it marks
// the site's older queued runs skipped (supersededDetail says why) and
// removes finished runs older than the newest keep of the site; queued and
// running runs are never removed.
func (s *Store) AddRun(ctx context.Context, r Run, supersededDetail string, keep int) (Run, error) {
	if r.Site == "" || r.Trigger == "" || r.SourceKind == "" || r.Ref == "" {
		return Run{}, invalidf("a run needs a site, a trigger, a source and a ref")
	}
	if !validCommitSHA(r.CommitSHA) {
		return Run{}, invalidf("%q is not a full commit id", r.CommitSHA)
	}
	if keep < 1 {
		keep = 1
	}
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sites WHERE name = ?`, r.Site).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return notFoundf("site not found")
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE site_runs SET status = ?, detail = ?, finished_at = ?
			WHERE site_name = ? AND status = ?`, RunSkipped, supersededDetail, now, r.Site, RunQueued); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO site_runs
			(site_name, trigger, status, source_kind, repository_id, repository, ref, commit_sha, message, author_name,
			 author_login, pusher, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Site, r.Trigger, RunQueued, r.SourceKind, r.RepositoryID, r.Repository, r.Ref, r.CommitSHA, r.Message,
			r.AuthorName, r.AuthorLogin, r.Pusher, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		// The oldest run that is kept; finished runs before it go.
		var oldest int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM site_runs WHERE site_name = ? ORDER BY id DESC LIMIT 1 OFFSET ?`,
			r.Site, keep-1).Scan(&oldest)
		if noRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM site_runs WHERE site_name = ? AND id < ? AND status IN (?, ?, ?, ?)`,
			r.Site, oldest, RunSucceeded, RunFailed, RunCancelled, RunSkipped)
		return err
	})
	if err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

// Run returns one run.
func (s *Store) Run(ctx context.Context, id int64) (Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM site_runs WHERE id = ?`, id))
}

// SiteRuns lists the newest runs of a site, newest first.
func (s *Store) SiteRuns(ctx context.Context, site string, limit int) ([]Run, error) {
	return scanRuns(s.db.QueryContext(ctx, `SELECT `+runCols+` FROM site_runs WHERE site_name = ?
		ORDER BY id DESC LIMIT ?`, site, limit))
}

// SitesWithQueuedRuns lists the sites that have a queued run, by name.
func (s *Store) SitesWithQueuedRuns(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT site_name FROM site_runs WHERE status = ? ORDER BY site_name`, RunQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// ClaimNextRun marks the newest queued run of a site running and returns
// it. ErrNotFound: nothing is queued. ErrConflict: a run of the site is
// already running. Older queued runs (there are none while AddRun
// supersedes them) are marked skipped.
func (s *Store) ClaimNextRun(ctx context.Context, site, supersededDetail string) (Run, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var running int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM site_runs WHERE site_name = ? AND status = ?`,
			site, RunRunning).Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			return conflictf("a run of the site is already running")
		}
		err := tx.QueryRowContext(ctx, `SELECT id FROM site_runs WHERE site_name = ? AND status = ?
			ORDER BY id DESC LIMIT 1`, site, RunQueued).Scan(&id)
		if noRows(err) {
			return notFoundf("no queued run")
		}
		if err != nil {
			return err
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE site_runs SET status = ?, detail = ?, finished_at = ?
			WHERE site_name = ? AND status = ? AND id <> ?`, RunSkipped, supersededDetail, now, site, RunQueued, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE site_runs SET status = ?, started_at = ? WHERE id = ?`, RunRunning, now, id)
		return err
	})
	if err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

// FinishRun gives a queued or running run its final status. ErrConflict if
// it already has one, ErrInvalid for a status that is not final.
func (s *Store) FinishRun(ctx context.Context, id int64, status, detail string) (Run, error) {
	if !RunFinished(status) {
		return Run{}, invalidf("%q is not a final status of a run", status)
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var cur string
		err := tx.QueryRowContext(ctx, `SELECT status FROM site_runs WHERE id = ?`, id).Scan(&cur)
		if noRows(err) {
			return notFoundf("run not found")
		}
		if err != nil {
			return err
		}
		if RunFinished(cur) {
			return conflictf("the run has already ended (%s)", cur)
		}
		_, err = tx.ExecContext(ctx, `UPDATE site_runs SET status = ?, detail = ?, finished_at = ? WHERE id = ?`,
			status, detail, s.now(), id)
		return err
	})
	if err != nil {
		return Run{}, err
	}
	return s.Run(ctx, id)
}

// ---- delivery ids ----

// ClaimDelivery records a GitHub delivery id before it is handled. False:
// the id was seen before (and not released), so the delivery is a repeat.
func (s *Store) ClaimDelivery(ctx context.Context, id string, appID int64) (bool, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO github_deliveries (id, app_id, received_at) VALUES (?, ?, ?)`,
		id, appID, s.now())
	if isUnique(err) {
		return false, nil
	}
	return err == nil, err
}

// ReleaseDelivery forgets a claimed delivery id whose handling failed, so
// the same delivery is handled when it is sent again.
func (s *Store) ReleaseDelivery(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM github_deliveries WHERE id = ?`, id)
	return err
}

// ForgetDeliveries removes delivery ids received before t, and the oldest
// beyond max (up to 10000 of those per call). It returns how many it
// removed.
func (s *Store) ForgetDeliveries(ctx context.Context, before time.Time, max int) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM github_deliveries ORDER BY received_at DESC, id DESC
		LIMIT 10000 OFFSET ?`, max)
	if err != nil {
		return 0, err
	}
	var extra []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		extra = append(extra, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	removed := 0
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM github_deliveries WHERE received_at < ?`, before.Unix())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		removed = int(n)
		for _, id := range extra {
			res, err := tx.ExecContext(ctx, `DELETE FROM github_deliveries WHERE id = ?`, id)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			removed += int(n)
		}
		return nil
	})
	return removed, err
}

// ---- repositories GitHub reported gone ----

func goneID(installationID, repositoryID int64) string {
	return fmt.Sprintf("%d/%d", installationID, repositoryID)
}

// SetRepositoryGone records whether GitHub reported that an installation
// no longer sees a repository.
func (s *Store) SetRepositoryGone(ctx context.Context, installationID, repositoryID int64, gone bool) error {
	id := goneID(installationID, repositoryID)
	if !gone {
		_, err := s.db.ExecContext(ctx, `DELETE FROM github_repositories_gone WHERE id = ?`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO github_repositories_gone (id, installation_id, repository_id, since)
		VALUES (?, ?, ?, ?)`, id, installationID, repositoryID, s.now())
	if isUnique(err) {
		return nil
	}
	return err
}

// RepositoryGone reports whether GitHub reported that an installation no
// longer sees a repository.
func (s *Store) RepositoryGone(ctx context.Context, installationID, repositoryID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM github_repositories_gone WHERE id = ?`,
		goneID(installationID, repositoryID)).Scan(&n)
	return n > 0, err
}

// ---- Apps and installations, as deliveries change them ----

// GitHubAppByAppID returns the App with GitHub's id appID.
func (s *Store) GitHubAppByAppID(ctx context.Context, appID int64) (GitHubApp, error) {
	return scanGitHubApp(s.db.QueryRowContext(ctx, `SELECT `+githubAppCols+githubAppFrom+` WHERE a.app_id = ?`, appID))
}

// DeleteGitHubInstallation forgets an installation of an App (the panel's
// id) and the repositories reported gone for it; sources linked through it
// show "connection missing". ErrNotFound if the App has no such
// installation.
func (s *Store) DeleteGitHubInstallation(ctx context.Context, appID, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM github_installations WHERE id = ? AND app_id = ?`, id, appID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundf("GitHub installation not found")
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM github_repositories_gone WHERE installation_id = ?`, id)
		return err
	})
}

// SetGitHubInstallationSuspended records whether an installation of an App
// (the panel's id) is suspended. ErrNotFound if the App has no such
// installation.
func (s *Store) SetGitHubInstallationSuspended(ctx context.Context, appID, id int64, suspended bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE github_installations SET suspended = ?, updated_at = ?
		WHERE id = ? AND app_id = ?`, suspended, s.now(), id, appID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("GitHub installation not found")
	}
	return nil
}
