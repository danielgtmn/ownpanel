package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/danielgtmn/ownpanel/internal/github"
	"github.com/danielgtmn/ownpanel/internal/store"
)

// A site's source: the repository and branch its code comes from, and
// whether a push publishes it. Linking only records the choice; fetching,
// building and publishing on push come with the webhook (#109) and builds
// on the server (#113). PublishingAvailable tells clients so.
//
// Who may do what:
//   - seeing a site's source: whoever may see the site;
//   - linking (or changing branch or "publish on push"), the same as
//     changing the site (owner or administrator, a write token, a token
//     limited to sites only for its sites) AND the repository must pass
//     UsableGitHubRepository for the actor at that moment. Every change
//     links anew, so it is checked again, and GitHub is asked whether the
//     repository and the branch exist;
//   - unlinking: the same as changing the site, without the repository
//     check (a member may unlink a repository an administrator linked).
//
// Removing a GitHub connection or installation keeps the source: its
// installation becomes unknown and the site shows "connection missing"
// with the repository it was linked to, until it is linked again or
// unlinked. Nothing is published from it meanwhile.

// PublishingAvailable reports whether a push to a linked branch publishes
// the site yet. False until building on the server exists (#113): a push
// records a run (runs.go) that waits.
const PublishingAvailable = false

// Source states.
const (
	SourceLinked            = "linked"
	SourceConnectionMissing = "connection_missing"
	SourceSuspended         = "suspended"
	SourceRepositoryMissing = "repository_missing"
	SourceUnchecked         = "unchecked"
)

// sourceCheckTimeout bounds asking GitHub about a source while showing it.
const sourceCheckTimeout = 15 * time.Second

// SiteSource is where a site's code comes from. Never secrets.
type SiteSource struct {
	Kind string `json:"kind"` // "github" | "git"
	// Repository is owner/name on GitHub (as last seen; refreshed when
	// the panel sees the repository), or the address of a Git repository.
	Repository     string `json:"repository"`
	RepositoryID   int64  `json:"repository_id,omitempty"`
	InstallationID int64  `json:"installation_id,omitempty"`
	// Account is the GitHub account of the installation, "" if unknown.
	Account string `json:"account,omitempty"`
	// URL is the repository's page, if known.
	URL           string `json:"url,omitempty"`
	Branch        string `json:"branch"`
	PublishOnPush bool   `json:"publish_on_push"`
	// Status: "linked", "connection_missing" (the GitHub connection or its
	// installation was removed), "suspended" (on GitHub),
	// "repository_missing" (the installation no longer sees it),
	// "unchecked" (GitHub could not be asked; Problem says why).
	Status   string    `json:"status"`
	Problem  string    `json:"problem,omitempty"`
	LinkedBy string    `json:"linked_by"`
	LinkedAt time.Time `json:"linked_at"`
}

// SiteSourceState is a site's source (nil: none) and whether publishing on
// push works yet.
type SiteSourceState struct {
	Site                string      `json:"site"`
	Source              *SiteSource `json:"source"`
	PublishingAvailable bool        `json:"publishing_available"`
	// LastRun is the site's newest run, nil if it has none (it may
	// outlive the source).
	LastRun *Run `json:"last_run"`
}

// SiteSourceInput links a site to a repository of a GitHub installation.
// Both ids come from LinkableRepositories; they prove nothing by
// themselves and are checked against GitHub.
type SiteSourceInput struct {
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	Branch         string `json:"branch"`
	PublishOnPush  bool   `json:"publish_on_push"`
}

// LinkableRepository is a repository the actor may link, with the account
// it belongs to.
type LinkableRepository struct {
	GitHubRepository
	Account string `json:"account"`
}

// LinkableRepositories lists what the actor may link a site to.
type LinkableRepositories struct {
	// Connected: at least one GitHub account is connected (only told to
	// those who may use it).
	Connected    bool                 `json:"connected"`
	Repositories []LinkableRepository `json:"repositories"`
	// Problems are installations that could not be listed.
	Problems  []string `json:"problems"`
	Truncated bool     `json:"truncated"`
}

// SourceBranches are the branches of a repository.
type SourceBranches struct {
	Branches      []string `json:"branches"`
	DefaultBranch string   `json:"default_branch"`
	Truncated     bool     `json:"truncated"`
}

// ValidateBranch checks a branch name before it goes anywhere near Git.
func ValidateBranch(b string) error {
	if b == "" {
		return errorf(Invalid, "choose a branch")
	}
	if len(b) > github.BranchMax {
		return errorf(Invalid, "the branch name is longer than %d characters", github.BranchMax)
	}
	if !github.ValidBranch(b) {
		return errorf(Invalid, "%q is not a valid branch name: no spaces, no \"..\", no leading \"-\" and none of ~ ^ : ? * [ \\", b)
	}
	return nil
}

// mayUseGitHub is the first half of UsableGitHubRepository's rule, for
// lists: administrators not limited to sites.
func mayUseGitHub(a Actor) bool {
	return a.authenticated() && a.Admin && a.Sites == nil
}

// ---- operations ----

// GetSiteSource returns a site's source. For GitHub it asks (through the
// cached repository list) whether the installation still sees the
// repository, and stores its current name.
func (s *Service) GetSiteSource(ctx context.Context, a Actor, site string) (SiteSourceState, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return SiteSourceState{}, err
	}
	out := SiteSourceState{Site: st.Name, PublishingAvailable: PublishingAvailable}
	if out.LastRun, err = s.lastRun(ctx, st.Name); err != nil {
		return SiteSourceState{}, storeError(err)
	}
	src, err := s.store.SiteSource(ctx, st.Name)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return SiteSourceState{}, storeError(err)
	}
	v := s.checkSource(ctx, src, mayUseGitHub(a))
	out.Source = &v
	return out, nil
}

func sourceView(src store.SiteSource) SiteSource {
	return SiteSource{
		Kind: src.Kind, Repository: src.Repository, RepositoryID: src.RepositoryID, InstallationID: src.InstallationID,
		Branch: src.Branch, PublishOnPush: src.PublishOnPush, Status: SourceLinked,
		LinkedBy: src.CreatedBy, LinkedAt: src.UpdatedAt,
	}
}

// checkSource fills in the status of a source. detail: the actor may see
// the GitHub connections, so problems may name them.
func (s *Service) checkSource(ctx context.Context, src store.SiteSource, detail bool) SiteSource {
	v := sourceView(src)
	problem := func(err error) string {
		if detail {
			return err.Error()
		}
		return "GitHub could not be asked about the repository right now"
	}
	if src.Kind != store.SourceGitHub {
		return v
	}
	if src.InstallationID == 0 {
		v.Status = SourceConnectionMissing
		return v
	}
	inst, app, err := s.installationApp(ctx, src.InstallationID)
	if err != nil {
		v.Status = SourceConnectionMissing
		if KindOf(err) != NotFound {
			v.Status, v.Problem = SourceUnchecked, problem(err)
		}
		return v
	}
	v.Account = inst.AccountLogin
	if inst.Suspended {
		v.Status = SourceSuspended
		return v
	}
	// What GitHub reported in a delivery holds while GitHub cannot be
	// asked.
	unchecked := func(err error) SiteSource {
		v.Status, v.Problem = SourceUnchecked, problem(err)
		if src.RepositoryGone {
			v.Status, v.Problem = SourceRepositoryMissing, ""
		}
		return v
	}
	auth, err := s.appAuth(app)
	if err != nil {
		return unchecked(err)
	}
	ctx, cancel := context.WithTimeout(ctx, sourceCheckTimeout)
	defer cancel()
	e, err := s.listRepositories(ctx, app, auth, inst.ID)
	if err != nil {
		return unchecked(err)
	}
	for _, r := range e.repos {
		if r.ID == src.RepositoryID {
			if src.RepositoryGone {
				// GitHub shows it again (added back to the installation).
				if _, err := s.store.MarkRepositoryGone(context.WithoutCancel(ctx), src.InstallationID, r.ID, false); err != nil {
					slog.Error("could not store that a repository is back", "repository", r.ID, "err", err)
				}
			}
			v.URL = r.HTMLURL
			if r.FullName != src.Repository {
				v.Repository = r.FullName
				if err := s.store.RenameSourceRepository(context.WithoutCancel(ctx), r.ID, r.FullName); err != nil {
					slog.Error("could not store the new name of a repository", "repository", r.ID, "err", err)
				}
			}
			return v
		}
	}
	if e.truncated {
		v.Status, v.Problem = SourceUnchecked, "the installation sees more repositories than the panel lists"
		return v
	}
	v.Status = SourceRepositoryMissing
	return v
}

// SetSiteSource links a site to a repository and branch, or changes the
// branch or "publish on push" of its link (which links it anew).
func (s *Service) SetSiteSource(ctx context.Context, a Actor, site string, in SiteSourceInput) (SiteSourceState, error) {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return SiteSourceState{}, err
	}
	if err := needWrite(a); err != nil {
		return SiteSourceState{}, err
	}
	if a.Via == ViaSystem {
		return SiteSourceState{}, errorf(Forbidden, "a site must be linked by a person")
	}
	if err := ValidateBranch(in.Branch); err != nil {
		return SiteSourceState{}, err
	}
	repo, err := s.UsableGitHubRepository(ctx, a, in.InstallationID, in.RepositoryID)
	if err != nil {
		return SiteSourceState{}, err
	}
	if repo.InstallationID != in.InstallationID || repo.ID != in.RepositoryID {
		return SiteSourceState{}, errorf(NotFound, "GitHub repository not found")
	}
	if _, err := s.githubBranch(ctx, repo, in.Branch); err != nil {
		return SiteSourceState{}, err
	}
	src, err := s.store.PutSiteSource(ctx, store.SiteSource{
		Site: st.Name, Kind: store.SourceGitHub, InstallationID: repo.InstallationID, RepositoryID: repo.ID,
		Repository: repo.FullName, Branch: in.Branch, PublishOnPush: in.PublishOnPush,
	}, a.UserID)
	if err != nil {
		return SiteSourceState{}, storeError(err)
	}
	push := "off"
	if in.PublishOnPush {
		push = "on"
	}
	s.record(ctx, a, "site.source", st.Name, fmt.Sprintf("%s, branch %s, publish on push %s", repo.FullName, in.Branch, push))
	v := sourceView(src)
	v.URL = repo.HTMLURL
	v.Account = repo.Owner
	if inst, err := s.store.GitHubInstallation(ctx, repo.InstallationID); err == nil {
		v.Account = inst.AccountLogin
	}
	out := SiteSourceState{Site: st.Name, Source: &v, PublishingAvailable: PublishingAvailable}
	if out.LastRun, err = s.lastRun(ctx, st.Name); err != nil {
		return SiteSourceState{}, storeError(err)
	}
	return out, nil
}

// RemoveSiteSource unlinks a site. Its published files stay.
func (s *Service) RemoveSiteSource(ctx context.Context, a Actor, site string) error {
	st, err := s.visibleSite(ctx, a, site)
	if err != nil {
		return err
	}
	if err := needWrite(a); err != nil {
		return err
	}
	src, err := s.store.SiteSource(ctx, st.Name)
	if err != nil {
		return storeError(err)
	}
	if err := s.store.DeleteSiteSource(ctx, st.Name); err != nil {
		return storeError(err)
	}
	s.record(ctx, a, "site.source-remove", st.Name, src.Repository)
	return nil
}

// LinkableRepositories lists the repositories the actor may link the site
// to: every repository of every installation for administrators, nothing
// for anyone else (UsableGitHubRepository's rule).
func (s *Service) LinkableRepositories(ctx context.Context, a Actor, site string) (LinkableRepositories, error) {
	if _, err := s.visibleSite(ctx, a, site); err != nil {
		return LinkableRepositories{}, err
	}
	out := LinkableRepositories{Repositories: []LinkableRepository{}, Problems: []string{}}
	if !mayUseGitHub(a) {
		return out, nil
	}
	insts, err := s.store.GitHubInstallations(ctx, 0)
	if err != nil {
		return LinkableRepositories{}, storeError(err)
	}
	out.Connected = len(insts) > 0
	if !out.Connected {
		apps, err := s.store.GitHubApps(ctx)
		if err != nil {
			return LinkableRepositories{}, storeError(err)
		}
		out.Connected = len(apps) > 0
	}
	for _, inst := range insts {
		if inst.Suspended {
			out.Problems = append(out.Problems, fmt.Sprintf("the GitHub app is suspended on %s", inst.AccountLogin))
			continue
		}
		list, err := s.GitHubRepositories(ctx, a, inst.ID)
		if err != nil {
			if KindOf(err) == Internal {
				return LinkableRepositories{}, err
			}
			out.Problems = append(out.Problems, fmt.Sprintf("%s: %s", inst.AccountLogin, err.Error()))
			continue
		}
		out.Truncated = out.Truncated || list.Truncated
		for _, r := range list.Repositories {
			out.Repositories = append(out.Repositories, LinkableRepository{GitHubRepository: r, Account: inst.AccountLogin})
		}
	}
	return out, nil
}

// SourceBranches lists the branches of a repository the actor may link
// the site to.
func (s *Service) SourceBranches(ctx context.Context, a Actor, site string, installationID, repositoryID int64) (SourceBranches, error) {
	if _, err := s.visibleSite(ctx, a, site); err != nil {
		return SourceBranches{}, err
	}
	repo, err := s.UsableGitHubRepository(ctx, a, installationID, repositoryID)
	if err != nil {
		return SourceBranches{}, err
	}
	tok, app, err := s.sourceToken(ctx, repo)
	if err != nil {
		return SourceBranches{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	list, truncated, err := s.github.Branches(ctx, tok, repo.Owner, repo.Name)
	if err != nil {
		return SourceBranches{}, githubError(app.Name, err)
	}
	out := SourceBranches{Branches: make([]string, 0, len(list)), DefaultBranch: repo.DefaultBranch, Truncated: truncated}
	for _, b := range list {
		out.Branches = append(out.Branches, b.Name)
	}
	return out, nil
}

// sourceToken is a token that may only read one repository's branches.
func (s *Service) sourceToken(ctx context.Context, repo GitHubRepository) (github.Token, store.GitHubApp, error) {
	_, app, err := s.installationApp(ctx, repo.InstallationID)
	if err != nil {
		return github.Token{}, store.GitHubApp{}, err
	}
	tok, err := s.githubReadToken(ctx, repo.InstallationID, repo.ID)
	return tok, app, err
}

// githubBranch asks GitHub whether the branch exists.
func (s *Service) githubBranch(ctx context.Context, repo GitHubRepository, branch string) (github.Branch, error) {
	tok, app, err := s.sourceToken(ctx, repo)
	if err != nil {
		return github.Branch{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	b, err := s.github.GetBranch(ctx, tok, repo.Owner, repo.Name, branch)
	if errors.Is(err, github.ErrNotFound) {
		return github.Branch{}, &Error{Kind: Invalid, Msg: fmt.Sprintf("%s has no branch %q", repo.FullName, branch), Err: err}
	}
	if err != nil {
		return github.Branch{}, githubError(app.Name, err)
	}
	return b, nil
}
