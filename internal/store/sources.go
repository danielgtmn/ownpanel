package store

import (
	"context"
	"errors"
	"time"

	"github.com/danielgtmn/ablage"
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
	CreatedBy    string // user name, "" if the user was removed
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

var errNoSource = notFoundf("the site has no source")

// checkSource stands for the table constraints from before ablage.
func checkSource(src SiteSource) error {
	switch src.Kind {
	case SourceGitHub:
		if src.RepositoryID <= 0 {
			return invalidf("a GitHub source needs GitHub's id of the repository")
		}
		if len(src.DeployKey) > 0 || len(src.NotifySecret) > 0 {
			return invalidf("a GitHub source has no deploy key")
		}
	case SourceGit:
		if src.InstallationID != 0 || src.RepositoryID != 0 {
			return invalidf("a Git source has no GitHub installation or repository")
		}
	default:
		return invalidf("unknown kind of source %q", src.Kind)
	}
	return nil
}

func sourcesOf(ctx context.Context, r repos, rows []siteSourceRow) ([]SiteSource, error) {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = deref(row.CreatedBy)
	}
	names, err := userNames(ctx, r, ids)
	if err != nil {
		return nil, err
	}
	out := make([]SiteSource, len(rows))
	for i, row := range rows {
		var inst int64
		if row.InstallationID != nil {
			if inst, err = parseInstallationKey(*row.InstallationID); err != nil {
				return nil, err
			}
		}
		out[i] = SiteSource{Site: row.SiteName, Kind: row.Kind, InstallationID: inst, RepositoryID: deref(row.RepositoryID),
			Repository: row.Repository, Branch: row.Branch, PublishOnPush: row.PublishOnPush, DeployKey: row.DeployKey,
			NotifySecret: row.NotifySecret, CreatedBy: names[deref(row.CreatedBy)], CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt}
	}
	return out, nil
}

func sourceRowOf(ctx context.Context, r repos, site string) (siteSourceRow, error) {
	row, err := r.sources.First(ctx, ablage.Query{Where: ablage.Eq("site_name", site)})
	return row, missing(err, errNoSource)
}

// SiteSource returns the source of a site; ErrNotFound if it has none.
func (s *Store) SiteSource(ctx context.Context, site string) (SiteSource, error) {
	row, err := sourceRowOf(ctx, s.r, site)
	if err != nil {
		return SiteSource{}, err
	}
	list, err := sourcesOf(ctx, s.r, []siteSourceRow{row})
	if err != nil {
		return SiteSource{}, err
	}
	return list[0], nil
}

// SiteSourcesOfRepository lists the sources linked to a GitHub repository,
// by site name.
func (s *Store) SiteSourcesOfRepository(ctx context.Context, repositoryID int64) ([]SiteSource, error) {
	rows, err := s.r.sources.Find(ctx, ablage.Query{
		Where:   ablage.And(ablage.Eq("kind", SourceGitHub), ablage.Eq("repository_id", repositoryID)),
		OrderBy: []ablage.Order{ablage.Asc("site_name")},
	})
	if err != nil {
		return nil, err
	}
	return sourcesOf(ctx, s.r, rows)
}

// PutSiteSource links a site to a source, replacing the one it had.
// by is the id of the user who links it ("" for none).
func (s *Store) PutSiteSource(ctx context.Context, src SiteSource, by string) (SiteSource, error) {
	if err := checkSource(src); err != nil {
		return SiteSource{}, err
	}
	now := s.now()
	row := siteSourceRow{SiteName: src.Site, Kind: src.Kind, Repository: src.Repository, Branch: src.Branch,
		PublishOnPush: src.PublishOnPush, DeployKey: src.DeployKey, NotifySecret: src.NotifySecret,
		CreatedBy: optional(by), CreatedAt: now, UpdatedAt: now}
	if src.InstallationID != 0 {
		row.InstallationID = ptr(installationKey(src.InstallationID))
	}
	if src.RepositoryID != 0 {
		row.RepositoryID = ptr(src.RepositoryID)
	}
	err := s.tx(ctx, func(r repos) error {
		if ok, err := r.sites.Exists(ctx, src.Site); err != nil {
			return err
		} else if !ok {
			return errSiteNotFound
		}
		have, err := sourceRowOf(ctx, r, src.Site)
		switch {
		case errors.Is(err, ErrNotFound):
			err = r.sources.Create(ctx, &row)
		case err != nil:
			return err
		default:
			row.ID, row.CreatedAt = have.ID, have.CreatedAt
			err = r.sources.Replace(ctx, &row)
		}
		if errors.Is(err, ablage.ErrReference) {
			// The installation or the user went away in between.
			return notFoundf("the GitHub installation or the user is gone")
		}
		return err
	})
	if err != nil {
		return SiteSource{}, err
	}
	return s.SiteSource(ctx, src.Site)
}

// RenameSourceRepository stores the name a GitHub repository has now.
func (s *Store) RenameSourceRepository(ctx context.Context, repositoryID int64, fullName string) error {
	_, err := s.r.sources.UpdateWhere(ctx, ablage.And(ablage.Eq("kind", SourceGitHub), ablage.Eq("repository_id", repositoryID),
		ablage.Ne("repository", fullName)), ablage.Row{"repository": fullName})
	return err
}

// DeleteSiteSource unlinks a site; ErrNotFound if it had no source.
func (s *Store) DeleteSiteSource(ctx context.Context, site string) error {
	n, err := s.r.sources.DeleteWhere(ctx, ablage.Eq("site_name", site))
	if err != nil {
		return err
	}
	if n == 0 {
		return errNoSource
	}
	return nil
}
