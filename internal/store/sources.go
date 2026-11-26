package store

import (
	"context"
	"database/sql"
	"time"
)

// Kinds of a site's source.
const (
	SourceGitHub = "github" // a repository of a GitHub App installation
	SourceGit    = "git"    // any Git host with a deploy key (#111)
)

// SiteSource is where a site's code comes from.
type SiteSource struct {
	Site string
	Kind string
	// InstallationID is GitHub's installation id; 0 once the installation
	// or its connection was removed.
	InstallationID int64
	// RepositoryID is GitHub's repository id (kind github).
	RepositoryID int64
	// Repository is owner/name for GitHub (as last seen), the clone
	// address for Git.
	Repository    string
	Branch        string
	PublishOnPush bool
	// DeployKey and NotifySecret are sealed (kind git, #111).
	DeployKey    []byte
	NotifySecret []byte
	// RepositoryGone: GitHub reported that the installation no longer sees
	// the repository (MarkRepositoryGone); linking again clears it.
	RepositoryGone bool
	CreatedBy      string // user name, "" if the user was removed
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const sourceCols = `s.site_name, s.kind, coalesce(s.installation_id, 0), coalesce(s.repository_id, 0), s.repository,
	s.branch, s.publish_on_push, s.deploy_key, s.notify_secret, s.repository_gone, coalesce(u.name, ''), s.created_at,
	s.updated_at`

const sourceFrom = ` FROM site_sources s LEFT JOIN users u ON u.id = s.created_by`

func scanSource(row interface{ Scan(...any) error }) (SiteSource, error) {
	var src SiteSource
	var created, updated int64
	err := row.Scan(&src.Site, &src.Kind, &src.InstallationID, &src.RepositoryID, &src.Repository, &src.Branch,
		&src.PublishOnPush, &src.DeployKey, &src.NotifySecret, &src.RepositoryGone, &src.CreatedBy, &created, &updated)
	if err != nil {
		if noRows(err) {
			return SiteSource{}, notFoundf("the site has no source")
		}
		return SiteSource{}, err
	}
	src.CreatedAt, src.UpdatedAt = unix(created), unix(updated)
	return src, nil
}

// SiteSource returns the source of a site; ErrNotFound if it has none.
func (s *Store) SiteSource(ctx context.Context, site string) (SiteSource, error) {
	return scanSource(s.db.QueryRowContext(ctx, `SELECT `+sourceCols+sourceFrom+` WHERE s.site_name = ?`, site))
}

// SiteSourcesOfRepository lists the sources linked to a GitHub repository,
// by site name.
func (s *Store) SiteSourcesOfRepository(ctx context.Context, repositoryID int64) ([]SiteSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceCols+sourceFrom+`
		WHERE s.kind = 'github' AND s.repository_id = ? ORDER BY s.site_name`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SiteSource{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// PutSiteSource links a site to a source, replacing the one it had.
// by is the user who links it.
func (s *Store) PutSiteSource(ctx context.Context, src SiteSource, by int64) (SiteSource, error) {
	if src.Kind != SourceGitHub && src.Kind != SourceGit {
		return SiteSource{}, invalidf("unknown kind of source %q", src.Kind)
	}
	now := s.now()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sites WHERE name = ?`, src.Site).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return notFoundf("site not found")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO site_sources
			(site_name, kind, installation_id, repository_id, repository, branch, publish_on_push, deploy_key, notify_secret,
			 created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (site_name) DO UPDATE SET kind = excluded.kind, installation_id = excluded.installation_id,
				repository_id = excluded.repository_id, repository = excluded.repository, branch = excluded.branch,
				publish_on_push = excluded.publish_on_push, deploy_key = excluded.deploy_key,
				notify_secret = excluded.notify_secret, repository_gone = 0, created_by = excluded.created_by,
				updated_at = excluded.updated_at`,
			src.Site, src.Kind, nullID(src.InstallationID), nullID(src.RepositoryID), src.Repository, src.Branch,
			src.PublishOnPush, src.DeployKey, src.NotifySecret, nullID(by), now, now)
		return err
	})
	if err != nil {
		return SiteSource{}, err
	}
	return s.SiteSource(ctx, src.Site)
}

// RenameSourceRepository stores the name a GitHub repository has now.
func (s *Store) RenameSourceRepository(ctx context.Context, repositoryID int64, fullName string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE site_sources SET repository = ?
		WHERE kind = 'github' AND repository_id = ? AND repository <> ?`, fullName, repositoryID, fullName)
	return err
}

// MarkRepositoryGone records whether GitHub reported that an installation
// no longer sees a repository (installationID 0: whichever installation
// the sources use). It returns how many sources changed.
func (s *Store) MarkRepositoryGone(ctx context.Context, installationID, repositoryID int64, gone bool) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE site_sources SET repository_gone = ?
		WHERE kind = 'github' AND repository_id = ? AND (? = 0 OR installation_id = ?) AND repository_gone <> ?`,
		gone, repositoryID, installationID, installationID, gone)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DeleteSiteSource unlinks a site; ErrNotFound if it had no source.
func (s *Store) DeleteSiteSource(ctx context.Context, site string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM site_sources WHERE site_name = ?`, site)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFoundf("the site has no source")
	}
	return nil
}
