package store

import (
	"context"
	"database/sql"
	"time"
)

// GitHubApp is a GitHub App the panel created. The three secrets are
// stored as given; the panel seals them before.
type GitHubApp struct {
	ID            int64 // the panel's id
	AppID         int64 // GitHub's id
	Slug          string
	Name          string
	ClientID      string
	OwnerLogin    string
	OwnerType     string
	HTMLURL       string
	PrivateKey    []byte
	WebhookSecret []byte
	ClientSecret  []byte
	CreatedBy     string // user name, "" if the user was removed
	CreatedAt     time.Time
	LastCheckedAt *time.Time
	LastError     string
}

// GitHubInstallation is where an App is installed.
type GitHubInstallation struct {
	ID                  int64 // GitHub's installation id
	AppID               int64 // the panel's id of the App
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	Suspended           bool
	RepositoryCount     *int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

const githubAppCols = `a.id, a.app_id, a.slug, a.name, a.client_id, a.owner_login, a.owner_type, a.html_url,
	a.private_key, a.webhook_secret, a.client_secret, coalesce(u.name, ''), a.created_at, a.last_checked_at, a.last_error`

const githubAppFrom = ` FROM github_apps a LEFT JOIN users u ON u.id = a.created_by`

func scanGitHubApp(row interface{ Scan(...any) error }) (GitHubApp, error) {
	var a GitHubApp
	var created int64
	var checked sql.NullInt64
	err := row.Scan(&a.ID, &a.AppID, &a.Slug, &a.Name, &a.ClientID, &a.OwnerLogin, &a.OwnerType, &a.HTMLURL,
		&a.PrivateKey, &a.WebhookSecret, &a.ClientSecret, &a.CreatedBy, &created, &checked, &a.LastError)
	if err != nil {
		if noRows(err) {
			return GitHubApp{}, notFoundf("GitHub connection not found")
		}
		return GitHubApp{}, err
	}
	a.CreatedAt = unix(created)
	if checked.Valid {
		at := unix(checked.Int64)
		a.LastCheckedAt = &at
	}
	return a, nil
}

// CreateGitHubApp stores an App. createdBy is the user who connected it.
func (s *Store) CreateGitHubApp(ctx context.Context, a GitHubApp, createdBy int64) (GitHubApp, error) {
	var by sql.NullInt64
	if createdBy != 0 {
		by = sql.NullInt64{Int64: createdBy, Valid: true}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO github_apps
		(app_id, slug, name, client_id, owner_login, owner_type, html_url, private_key, webhook_secret, client_secret,
		 created_by, created_at, last_checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.AppID, a.Slug, a.Name, a.ClientID, a.OwnerLogin, a.OwnerType, a.HTMLURL, a.PrivateKey, a.WebhookSecret,
		a.ClientSecret, by, s.now(), s.now())
	if isUnique(err) {
		return GitHubApp{}, conflictf("this GitHub App is already connected")
	}
	if err != nil {
		return GitHubApp{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return GitHubApp{}, err
	}
	return s.GitHubApp(ctx, id)
}

// GitHubApp returns one App by the panel's id.
func (s *Store) GitHubApp(ctx context.Context, id int64) (GitHubApp, error) {
	return scanGitHubApp(s.db.QueryRowContext(ctx, `SELECT `+githubAppCols+githubAppFrom+` WHERE a.id = ?`, id))
}

// GitHubAppByAppID returns the App with GitHub's id appID.
func (s *Store) GitHubAppByAppID(ctx context.Context, appID int64) (GitHubApp, error) {
	return scanGitHubApp(s.db.QueryRowContext(ctx, `SELECT `+githubAppCols+githubAppFrom+` WHERE a.app_id = ?`, appID))
}

// GitHubApps lists every App, oldest first.
func (s *Store) GitHubApps(ctx context.Context) ([]GitHubApp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+githubAppCols+githubAppFrom+` ORDER BY a.created_at, a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitHubApp{}
	for rows.Next() {
		a, err := scanGitHubApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteGitHubApp forgets an App, its secrets and its installations.
func (s *Store) DeleteGitHubApp(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM github_apps WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("GitHub connection not found")
	}
	return nil
}

// RecordGitHubCheck stores when GitHub was last asked about the App and
// what went wrong ("" if nothing).
func (s *Store) RecordGitHubCheck(ctx context.Context, id int64, at time.Time, lastError string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE github_apps SET last_checked_at = ?, last_error = ? WHERE id = ?`,
		at.Unix(), lastError, id)
	return err
}

const githubInstCols = `id, app_id, account_login, account_type, repository_selection, suspended, repository_count,
	created_at, updated_at`

func scanGitHubInstallation(row interface{ Scan(...any) error }) (GitHubInstallation, error) {
	var i GitHubInstallation
	var count sql.NullInt64
	var created, updated int64
	err := row.Scan(&i.ID, &i.AppID, &i.AccountLogin, &i.AccountType, &i.RepositorySelection, &i.Suspended, &count,
		&created, &updated)
	if err != nil {
		if noRows(err) {
			return GitHubInstallation{}, notFoundf("GitHub installation not found")
		}
		return GitHubInstallation{}, err
	}
	if count.Valid {
		n := int(count.Int64)
		i.RepositoryCount = &n
	}
	i.CreatedAt, i.UpdatedAt = unix(created), unix(updated)
	return i, nil
}

// GitHubInstallation returns one installation by GitHub's id.
func (s *Store) GitHubInstallation(ctx context.Context, id int64) (GitHubInstallation, error) {
	return scanGitHubInstallation(s.db.QueryRowContext(ctx, `SELECT `+githubInstCols+` FROM github_installations WHERE id = ?`, id))
}

// GitHubInstallations lists the installations of one App, or of all if
// appID is 0, by account.
func (s *Store) GitHubInstallations(ctx context.Context, appID int64) ([]GitHubInstallation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+githubInstCols+` FROM github_installations
		WHERE ? = 0 OR app_id = ? ORDER BY lower(account_login), id`, appID, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitHubInstallation{}
	for rows.Next() {
		i, err := scanGitHubInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func upsertGitHubInstallation(ctx context.Context, tx *sql.Tx, i GitHubInstallation, now int64) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO github_installations
		(id, app_id, account_login, account_type, repository_selection, suspended, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET account_login = excluded.account_login, account_type = excluded.account_type,
			repository_selection = excluded.repository_selection, suspended = excluded.suspended,
			updated_at = excluded.updated_at
		WHERE github_installations.app_id = excluded.app_id`,
		i.ID, i.AppID, i.AccountLogin, i.AccountType, i.RepositorySelection, i.Suspended, now, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return conflictf("the installation %d belongs to another GitHub connection", i.ID)
	}
	return nil
}

// PutGitHubInstallation adds or updates one installation of an App.
func (s *Store) PutGitHubInstallation(ctx context.Context, i GitHubInstallation) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return upsertGitHubInstallation(ctx, tx, i, s.now()) })
}

// ReplaceGitHubInstallations makes list the installations of an App:
// others are removed, known ones keep their repository count.
func (s *Store) ReplaceGitHubInstallations(ctx context.Context, appID int64, list []GitHubInstallation) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		keep := map[int64]bool{}
		for _, i := range list {
			i.AppID = appID
			if err := upsertGitHubInstallation(ctx, tx, i, s.now()); err != nil {
				return err
			}
			keep[i.ID] = true
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM github_installations WHERE app_id = ?`, appID)
		if err != nil {
			return err
		}
		var gone []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if !keep[id] {
				gone = append(gone, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range gone {
			if _, err := tx.ExecContext(ctx, `DELETE FROM github_installations WHERE id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetGitHubRepositoryCount stores how many repositories an installation
// may see.
func (s *Store) SetGitHubRepositoryCount(ctx context.Context, id int64, n int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE github_installations SET repository_count = ? WHERE id = ?`, n, id)
	return err
}

// DeleteGitHubInstallation forgets an installation of an App (the panel's
// id); sources linked through it show "connection missing". ErrNotFound if
// the App has no such installation.
func (s *Store) DeleteGitHubInstallation(ctx context.Context, appID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM github_installations WHERE id = ? AND app_id = ?`, id, appID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("GitHub installation not found")
	}
	return nil
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
