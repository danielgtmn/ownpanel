package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtmn/ablage"
)

// GitHub connections: GitHub Apps the panel created from a manifest, and
// where they are installed. A private App can only be installed on the
// account that owns it, so connecting several GitHub accounts or
// organisations means one App each; nothing here limits their number.

// Account types GitHub reports for the owner of an App and of an
// installation.
const (
	githubUser         = "User"
	githubOrganization = "Organization"
)

// GitHubApp is a GitHub App the panel created. The three secrets are
// stored as given; the panel seals them before.
type GitHubApp struct {
	ID            string // the panel's id
	AppID         int64  // GitHub's id
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
	ID                  int64  // GitHub's installation id
	AppID               string // the panel's id of the App
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	Suspended           bool
	RepositoryCount     *int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

var (
	errGitHubAppNotFound          = notFoundf("GitHub connection not found")
	errGitHubInstallationNotFound = notFoundf("GitHub installation not found")
)

// installationKey is the row id of an installation: GitHub's id as text.
func installationKey(id int64) string { return strconv.FormatInt(id, 10) }

func parseInstallationKey(key string) (int64, error) { return strconv.ParseInt(key, 10, 64) }

func checkAccountType(t string) error {
	if t != githubUser && t != githubOrganization {
		return invalidf("unknown kind of GitHub account %q", t)
	}
	return nil
}

func checkGitHubApp(a GitHubApp) error {
	if a.AppID <= 0 {
		return invalidf("a GitHub App needs GitHub's id")
	}
	return checkAccountType(a.OwnerType)
}

func checkGitHubInstallation(i GitHubInstallation) error {
	if i.ID <= 0 {
		return invalidf("a GitHub installation needs GitHub's id")
	}
	if err := checkAccountType(i.AccountType); err != nil {
		return err
	}
	if i.RepositorySelection != "all" && i.RepositorySelection != "selected" {
		return invalidf("unknown repository selection %q", i.RepositorySelection)
	}
	return nil
}

func githubAppsOf(ctx context.Context, r repos, rows []githubAppRow) ([]GitHubApp, error) {
	ids := make([]string, len(rows))
	for i, a := range rows {
		ids[i] = deref(a.CreatedBy)
	}
	names, err := userNames(ctx, r, ids)
	if err != nil {
		return nil, err
	}
	out := make([]GitHubApp, len(rows))
	for i, a := range rows {
		out[i] = GitHubApp{ID: a.ID, AppID: a.AppID, Slug: a.Slug, Name: a.Name, ClientID: a.ClientID,
			OwnerLogin: a.OwnerLogin, OwnerType: a.OwnerType, HTMLURL: a.HTMLURL, PrivateKey: a.PrivateKey,
			WebhookSecret: a.WebhookSecret, ClientSecret: a.ClientSecret, CreatedBy: names[deref(a.CreatedBy)],
			CreatedAt: a.CreatedAt, LastCheckedAt: a.LastCheckedAt, LastError: a.LastError}
	}
	return out, nil
}

// CreateGitHubApp stores an App. createdBy is the id of the user who
// connected it ("" for none).
func (s *Store) CreateGitHubApp(ctx context.Context, a GitHubApp, createdBy string) (GitHubApp, error) {
	if err := checkGitHubApp(a); err != nil {
		return GitHubApp{}, err
	}
	now := s.now()
	row := githubAppRow{AppID: a.AppID, Slug: a.Slug, Name: a.Name, ClientID: a.ClientID, OwnerLogin: a.OwnerLogin,
		OwnerType: a.OwnerType, HTMLURL: a.HTMLURL, PrivateKey: a.PrivateKey, WebhookSecret: a.WebhookSecret,
		ClientSecret: a.ClientSecret, CreatedBy: optional(createdBy), CreatedAt: now, LastCheckedAt: &now}
	if err := s.r.githubApps.Create(ctx, &row); err != nil {
		if errors.Is(err, ablage.ErrReference) {
			return GitHubApp{}, errUserNotFound
		}
		return GitHubApp{}, dup(err, conflictf("this GitHub App is already connected"))
	}
	return s.GitHubApp(ctx, row.ID)
}

// GitHubApp returns one App by the panel's id.
func (s *Store) GitHubApp(ctx context.Context, id string) (GitHubApp, error) {
	if id == "" {
		return GitHubApp{}, errGitHubAppNotFound
	}
	row, err := s.r.githubApps.Get(ctx, id)
	if err != nil {
		return GitHubApp{}, missing(err, errGitHubAppNotFound)
	}
	list, err := githubAppsOf(ctx, s.r, []githubAppRow{row})
	if err != nil {
		return GitHubApp{}, err
	}
	return list[0], nil
}

// GitHubApps lists every App, oldest first.
func (s *Store) GitHubApps(ctx context.Context) ([]GitHubApp, error) {
	rows, err := s.r.githubApps.Find(ctx, ablage.Query{OrderBy: []ablage.Order{ablage.Asc("created_at")}})
	if err != nil {
		return nil, err
	}
	return githubAppsOf(ctx, s.r, rows)
}

// DeleteGitHubApp forgets an App, its secrets and its installations.
func (s *Store) DeleteGitHubApp(ctx context.Context, id string) error {
	if id == "" {
		return errGitHubAppNotFound
	}
	return missing(s.r.githubApps.Delete(ctx, id), errGitHubAppNotFound)
}

// RecordGitHubCheck stores when GitHub was last asked about the App and
// what went wrong ("" if nothing).
func (s *Store) RecordGitHubCheck(ctx context.Context, id string, at time.Time, lastError string) error {
	_, err := s.r.githubApps.UpdateWhere(ctx, ablage.Eq("id", id), ablage.Row{"last_checked_at": seconds(at), "last_error": lastError})
	return err
}

func installationOf(row githubInstallationRow) (GitHubInstallation, error) {
	id, err := parseInstallationKey(row.ID)
	if err != nil {
		return GitHubInstallation{}, err
	}
	return GitHubInstallation{ID: id, AppID: row.AppID, AccountLogin: row.AccountLogin, AccountType: row.AccountType,
		RepositorySelection: row.RepositorySelection, Suspended: row.Suspended, RepositoryCount: row.RepositoryCount,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

// GitHubInstallation returns one installation by GitHub's id.
func (s *Store) GitHubInstallation(ctx context.Context, id int64) (GitHubInstallation, error) {
	row, err := s.r.githubInsts.Get(ctx, installationKey(id))
	if err != nil {
		return GitHubInstallation{}, missing(err, errGitHubInstallationNotFound)
	}
	return installationOf(row)
}

// GitHubInstallations lists the installations of one App, or of all if
// appID is "", by account (ignoring case), then GitHub's id.
func (s *Store) GitHubInstallations(ctx context.Context, appID string) ([]GitHubInstallation, error) {
	where := ablage.All
	if appID != "" {
		where = ablage.Eq("app_id", appID)
	}
	rows, err := s.r.githubInsts.Find(ctx, ablage.Query{Where: where})
	if err != nil {
		return nil, err
	}
	out := make([]GitHubInstallation, 0, len(rows))
	for _, row := range rows {
		i, err := installationOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	slices.SortFunc(out, func(a, b GitHubInstallation) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.AccountLogin), strings.ToLower(b.AccountLogin)), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// putInstallation adds or updates one installation of an App. An
// installation of another App is never taken over; a known one keeps its
// repository count and when it was first seen.
func putInstallation(ctx context.Context, r repos, i GitHubInstallation, now time.Time) error {
	if err := checkGitHubInstallation(i); err != nil {
		return err
	}
	key := installationKey(i.ID)
	have, err := r.githubInsts.Get(ctx, key)
	switch {
	case errors.Is(err, ablage.ErrNotFound):
		err := r.githubInsts.Create(ctx, &githubInstallationRow{ID: key, AppID: i.AppID, AccountLogin: i.AccountLogin,
			AccountType: i.AccountType, RepositorySelection: i.RepositorySelection, Suspended: i.Suspended,
			CreatedAt: now, UpdatedAt: now})
		if errors.Is(err, ablage.ErrReference) {
			return errGitHubAppNotFound
		}
		return err
	case err != nil:
		return err
	}
	if have.AppID != i.AppID {
		return conflictf("the installation %d belongs to another GitHub connection", i.ID)
	}
	return r.githubInsts.Patch(ctx, key, ablage.Row{"account_login": i.AccountLogin, "account_type": i.AccountType,
		"repository_selection": i.RepositorySelection, "suspended": i.Suspended, "updated_at": now})
}

// PutGitHubInstallation adds or updates one installation of an App.
func (s *Store) PutGitHubInstallation(ctx context.Context, i GitHubInstallation) error {
	now := s.now()
	return s.tx(ctx, func(r repos) error { return putInstallation(ctx, r, i, now) })
}

// ReplaceGitHubInstallations makes list the installations of an App:
// others are removed, known ones keep their repository count.
func (s *Store) ReplaceGitHubInstallations(ctx context.Context, appID string, list []GitHubInstallation) error {
	now := s.now()
	return s.tx(ctx, func(r repos) error {
		keep := make([]string, 0, len(list))
		for _, i := range list {
			i.AppID = appID
			if err := putInstallation(ctx, r, i, now); err != nil {
				return err
			}
			keep = append(keep, installationKey(i.ID))
		}
		_, err := r.githubInsts.DeleteWhere(ctx, ablage.And(ablage.Eq("app_id", appID), ablage.Not(ablage.In("id", keep...))))
		return err
	})
}

// SetGitHubRepositoryCount stores how many repositories an installation
// may see.
func (s *Store) SetGitHubRepositoryCount(ctx context.Context, id int64, n int) error {
	_, err := s.r.githubInsts.UpdateWhere(ctx, ablage.Eq("id", installationKey(id)), ablage.Row{"repository_count": n})
	return err
}
