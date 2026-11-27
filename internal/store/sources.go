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
	// ID is the row's id (set when read).
	ID   string
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
	// Kind git (#111): DeployKey and NotifySecret are sealed by the panel;
	// DeployPublicKey, HostKey and HostKeyNew are authorized_keys lines
	// (empty for HTTPS addresses); NotifyID is the random id of the
	// notification address.
	DeployKey       []byte
	DeployPublicKey string
	HostKey         string
	HostKeyNew      string
	NotifySecret    []byte
	NotifyID        string
	// CheckedAt is when the panel last reached the repository (nil:
	// never), CheckError what went wrong then ("" if it worked).
	CheckedAt  *time.Time
	CheckError string
	CreatedBy  string // user name, "" if the user was removed
	CreatedAt  time.Time
	UpdatedAt  time.Time
	// PullRequestPreviews: pull requests from the same repository against
	// Branch are built as previews (on by default). ForkPreviews: those
	// from forks too (off by default). PutSiteSource keeps both of the
	// link it replaces; SetSourcePreviews changes them.
	PullRequestPreviews bool
	ForkPreviews        bool
}

var errNoSource = notFoundf("the site has no source")

// checkSource stands for the table constraints from before ablage.
func checkSource(src SiteSource) error {
	switch src.Kind {
	case SourceGitHub:
		if src.RepositoryID <= 0 {
			return invalidf("a GitHub source needs GitHub's id of the repository")
		}
		if len(src.DeployKey) > 0 || len(src.NotifySecret) > 0 || src.DeployPublicKey != "" || src.HostKey != "" ||
			src.HostKeyNew != "" || src.NotifyID != "" {
			return invalidf("a GitHub source has no deploy key")
		}
	case SourceGit:
		if src.InstallationID != 0 || src.RepositoryID != 0 {
			return invalidf("a Git source has no GitHub installation or repository")
		}
		if src.Repository == "" || src.NotifyID == "" || len(src.NotifySecret) == 0 {
			return invalidf("a Git source needs an address and a notification secret")
		}
		ssh := len(src.DeployKey) > 0
		if ssh != (src.DeployPublicKey != "") || ssh != (src.HostKey != "") {
			return invalidf("a Git source over SSH needs a deploy key and the server's key, one over HTTPS neither")
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
		out[i] = SiteSource{ID: row.ID, Site: row.SiteName, Kind: row.Kind, InstallationID: inst, RepositoryID: deref(row.RepositoryID),
			Repository: row.Repository, Branch: row.Branch, PublishOnPush: row.PublishOnPush, DeployKey: row.DeployKey,
			NotifySecret: row.NotifySecret, CreatedBy: names[deref(row.CreatedBy)], CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt, DeployPublicKey: row.DeployPublicKey, HostKey: row.HostKey, HostKeyNew: row.HostKeyNew,
			NotifyID: deref(row.NotifyID), CheckedAt: row.CheckedAt, CheckError: row.CheckError,
			PullRequestPreviews: row.PullRequestPreviews == nil || *row.PullRequestPreviews, ForkPreviews: row.ForkPreviews}
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

// GitHubSiteSources lists every source linked to a GitHub repository, by
// site name.
func (s *Store) GitHubSiteSources(ctx context.Context) ([]SiteSource, error) {
	rows, err := s.r.sources.Find(ctx, ablage.Query{
		Where:   ablage.Eq("kind", SourceGitHub),
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
		CreatedBy: optional(by), CreatedAt: now, UpdatedAt: now, DeployPublicKey: src.DeployPublicKey, HostKey: src.HostKey,
		HostKeyNew: src.HostKeyNew, NotifyID: optional(src.NotifyID), CheckedAt: src.CheckedAt, CheckError: src.CheckError}
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
			// Linking anew keeps the settings for pull requests.
			row.ID, row.CreatedAt = have.ID, have.CreatedAt
			row.PullRequestPreviews, row.ForkPreviews = have.PullRequestPreviews, have.ForkPreviews
			err = r.sources.Replace(ctx, &row)
		}
		if errors.Is(err, ablage.ErrReference) {
			// The installation or the user went away in between.
			return notFoundf("the GitHub installation or the user is gone")
		}
		if errors.Is(err, ablage.ErrDuplicate) {
			return conflictf("the notification address is already in use; try again")
		}
		return err
	})
	if err != nil {
		return SiteSource{}, err
	}
	return s.SiteSource(ctx, src.Site)
}

// SetSourcePreviews changes whether pull requests are built as previews:
// those from the same repository (pullRequests) and those from forks
// (forks). ErrNotFound if the site has no source.
func (s *Store) SetSourcePreviews(ctx context.Context, site string, pullRequests, forks bool) (SiteSource, error) {
	n, err := s.r.sources.UpdateWhere(ctx, ablage.Eq("site_name", site),
		ablage.Row{"pull_request_previews": pullRequests, "fork_previews": forks})
	if err != nil {
		return SiteSource{}, err
	}
	if n == 0 {
		return SiteSource{}, errNoSource
	}
	return s.SiteSource(ctx, site)
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

// ---- kind git (#111) ----

// GitSourceByNotifyID returns the Git source whose notification address
// has the id; ErrNotFound if none has.
func (s *Store) GitSourceByNotifyID(ctx context.Context, id string) (SiteSource, error) {
	if id == "" {
		return SiteSource{}, errNoSource
	}
	row, err := s.r.sources.First(ctx, ablage.Query{Where: ablage.And(ablage.Eq("kind", SourceGit), ablage.Eq("notify_id", id))})
	if err != nil {
		return SiteSource{}, missing(err, errNoSource)
	}
	list, err := sourcesOf(ctx, s.r, []siteSourceRow{row})
	if err != nil {
		return SiteSource{}, err
	}
	return list[0], nil
}

// gitSourceWhere matches the Git source of a site with the row id id, so a
// change made while the site was linked anew does not touch the new link.
func gitSourceWhere(site, id string) ablage.Cond {
	return ablage.And(ablage.Eq("site_name", site), ablage.Eq("kind", SourceGit), ablage.Eq("id", id))
}

func (s *Store) patchGitSource(ctx context.Context, site, id string, patch ablage.Row) error {
	n, err := s.r.sources.UpdateWhere(ctx, gitSourceWhere(site, id), patch)
	if err != nil {
		return err
	}
	if n == 0 {
		return errNoSource
	}
	return nil
}

// SetGitSourceCheck records the result of reaching a Git source's
// repository: checkError "" when it worked; hostKeyNew is the different key
// the server showed ("" if it showed the confirmed one).
func (s *Store) SetGitSourceCheck(ctx context.Context, site, id, checkError, hostKeyNew string) error {
	return s.patchGitSource(ctx, site, id, ablage.Row{"checked_at": s.now(), "check_error": checkError, "host_key_new": hostKeyNew})
}

// SetGitHostKey pins another server key for a Git source over SSH.
func (s *Store) SetGitHostKey(ctx context.Context, site, id, hostKey string) error {
	if hostKey == "" {
		return invalidf("a Git source over SSH needs the server's key")
	}
	return s.patchGitSource(ctx, site, id, ablage.Row{"host_key": hostKey, "host_key_new": "", "check_error": "",
		"updated_at": s.now()})
}

// SetGitDeployKey replaces the deploy key of a Git source over SSH.
func (s *Store) SetGitDeployKey(ctx context.Context, site, id string, sealed []byte, public string) error {
	if len(sealed) == 0 || public == "" {
		return invalidf("a deploy key needs both halves")
	}
	return s.patchGitSource(ctx, site, id, ablage.Row{"deploy_key": sealed, "deploy_public_key": public, "check_error": "",
		"checked_at": nil, "updated_at": s.now()})
}

// SetGitNotifySecret replaces the notification address and secret of a
// Git source.
func (s *Store) SetGitNotifySecret(ctx context.Context, site, id, notifyID string, sealed []byte) error {
	if notifyID == "" || len(sealed) == 0 {
		return invalidf("a notification address needs an id and a secret")
	}
	err := s.patchGitSource(ctx, site, id, ablage.Row{"notify_id": notifyID, "notify_secret": sealed, "updated_at": s.now()})
	if errors.Is(err, ablage.ErrDuplicate) {
		return conflictf("the notification address is already in use; try again")
	}
	return err
}

// ClaimGitDelivery records a Git host's delivery id for a source before it
// is handled. False: the id was seen before (and not released).
func (s *Store) ClaimGitDelivery(ctx context.Context, sourceID, delivery string) (bool, error) {
	if sourceID == "" || delivery == "" {
		return false, invalidf("a delivery needs a source and an id")
	}
	err := s.r.gitDelivs.Create(ctx, &gitDeliveryRow{ID: sourceID + "/" + delivery, SourceID: sourceID, ReceivedAt: s.now()})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ablage.ErrDuplicate):
		return false, nil
	case errors.Is(err, ablage.ErrReference):
		return false, errNoSource
	}
	return false, err
}

// ReleaseGitDelivery forgets a claimed delivery id whose handling failed.
func (s *Store) ReleaseGitDelivery(ctx context.Context, sourceID, delivery string) error {
	_, err := s.r.gitDelivs.DeleteWhere(ctx, ablage.Eq("id", sourceID+"/"+delivery))
	return err
}

// ForgetGitDeliveries removes delivery ids received before t, and the
// oldest beyond max (up to 10000 of those per call).
func (s *Store) ForgetGitDeliveries(ctx context.Context, before time.Time, max int) (int, error) {
	removed := 0
	err := s.tx(ctx, func(r repos) error {
		extra, err := r.gitDelivs.Find(ctx, ablage.Query{
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
		n, err := r.gitDelivs.DeleteWhere(ctx, ablage.Or(ablage.Lt("received_at", seconds(before)), ablage.In("id", ids...)))
		removed = int(n)
		return err
	})
	return removed, err
}
