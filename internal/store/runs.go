package store

import (
	"context"
	"database/sql"
	"time"
)

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
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

const runCols = `id, site_name, trigger, status, source_kind, coalesce(repository_id, 0), repository, ref, commit_sha,
	message, author_name, author_login, pusher, detail, created_at, started_at, finished_at`

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var created int64
	var started, finished sql.NullInt64
	err := row.Scan(&r.ID, &r.Site, &r.Trigger, &r.Status, &r.SourceKind, &r.RepositoryID, &r.Repository, &r.Ref,
		&r.CommitSHA, &r.Message, &r.AuthorName, &r.AuthorLogin, &r.Pusher, &r.Detail, &created, &started, &finished)
	if err != nil {
		if noRows(err) {
			return Run{}, notFoundf("run not found")
		}
		return Run{}, err
	}
	r.CreatedAt = unix(created)
	if started.Valid {
		t := unix(started.Int64)
		r.StartedAt = &t
	}
	if finished.Valid {
		t := unix(finished.Int64)
		r.FinishedAt = &t
	}
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

// AddRun records a queued run of a site. In the same transaction it marks
// the site's older queued runs skipped (supersededDetail says why) and
// removes finished runs beyond the newest keep of the site; queued and
// running runs are never removed.
func (s *Store) AddRun(ctx context.Context, r Run, supersededDetail string, keep int) (Run, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE site_runs SET status = 'skipped', detail = ?, finished_at = ?
			WHERE site_name = ? AND status = 'queued'`, supersededDetail, now, r.Site); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO site_runs
			(site_name, trigger, status, source_kind, repository_id, repository, ref, commit_sha, message, author_name,
			 author_login, pusher, created_at)
			VALUES (?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Site, r.Trigger, r.SourceKind, nullID(r.RepositoryID), r.Repository, r.Ref, r.CommitSHA, r.Message,
			r.AuthorName, r.AuthorLogin, r.Pusher, now)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, `DELETE FROM site_runs WHERE site_name = ?
			AND status NOT IN ('queued', 'running')
			AND id NOT IN (SELECT id FROM site_runs WHERE site_name = ? ORDER BY id DESC LIMIT ?)`, r.Site, r.Site, keep)
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
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT site_name FROM site_runs WHERE status = 'queued' ORDER BY site_name`)
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
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM site_runs WHERE site_name = ? AND status = 'running'`,
			site).Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			return conflictf("a run of the site is already running")
		}
		err := tx.QueryRowContext(ctx, `SELECT id FROM site_runs WHERE site_name = ? AND status = 'queued'
			ORDER BY id DESC LIMIT 1`, site).Scan(&id)
		if noRows(err) {
			return notFoundf("no queued run")
		}
		if err != nil {
			return err
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE site_runs SET status = 'skipped', detail = ?, finished_at = ?
			WHERE site_name = ? AND status = 'queued' AND id <> ?`, supersededDetail, now, site, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE site_runs SET status = 'running', started_at = ? WHERE id = ?`, now, id)
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

// ClaimDelivery records a GitHub delivery id before it is handled. False:
// the id was seen before (and not released), so the delivery is a repeat.
func (s *Store) ClaimDelivery(ctx context.Context, id string, appID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO github_deliveries (id, app_id, received_at) VALUES (?, ?, ?)
		ON CONFLICT (id) DO NOTHING`, id, appID, s.now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReleaseDelivery forgets a claimed delivery id whose handling failed, so
// the same delivery is handled when it is sent again.
func (s *Store) ReleaseDelivery(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM github_deliveries WHERE id = ?`, id)
	return err
}

// ForgetDeliveries removes deliveries received before t, and the oldest
// beyond max. It returns how many it removed.
func (s *Store) ForgetDeliveries(ctx context.Context, before time.Time, max int) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM github_deliveries WHERE received_at < ?
		OR id NOT IN (SELECT id FROM github_deliveries ORDER BY received_at DESC, rowid DESC LIMIT ?)`, before.Unix(), max)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
