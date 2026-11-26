package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// A site's source: the repository and branch its code comes from. These
// tools ask GitHub, so they are marked as reaching outside the panel.

type sourceInfo struct {
	Kind           string `json:"kind" jsonschema:"github: a repository of a connected GitHub account; git: any Git repository by address"`
	Repository     string `json:"repository" jsonschema:"owner/name on GitHub (its current name; repositories can be renamed), or the address of a Git repository"`
	RepositoryID   int64  `json:"repository_id,omitempty" jsonschema:"GitHub's id of the repository"`
	InstallationID int64  `json:"installation_id,omitempty" jsonschema:"installation the repository is reached through; 0 if its GitHub connection was removed"`
	Account        string `json:"account,omitempty" jsonschema:"GitHub account the repository is reached through"`
	URL            string `json:"url,omitempty" jsonschema:"the repository on GitHub"`
	Branch         string `json:"branch"`
	PublishOnPush  bool   `json:"publish_on_push" jsonschema:"a push to the branch should publish the site (see publishing_available)"`
	Status         string `json:"status" jsonschema:"linked; connection_missing (the GitHub connection was removed: link again or unlink); suspended (on GitHub); repository_missing (the connection no longer sees the repository); unchecked (GitHub could not be asked, see problem); for git: not_checked (not reached since linking: add the deploy key, then check_git_site_source), host_key_changed (the server shows new_host_key instead of host_key; a person must check and accept it in the web interface), access_failed (see problem)"`
	Problem        string `json:"problem,omitempty"`
	LinkedBy       string `json:"linked_by"`
	Server         string `json:"server,omitempty" jsonschema:"git: the Git server"`
	Transport      string `json:"transport,omitempty" jsonschema:"git: ssh or https"`
	DeployKey      string `json:"deploy_key,omitempty" jsonschema:"git over ssh: the public deploy key to add to the repository at the Git host, with read access only"`
	HostKey        string `json:"host_key,omitempty" jsonschema:"git over ssh: fingerprint of the server key confirmed for the site"`
	NewHostKey     string `json:"new_host_key,omitempty" jsonschema:"git over ssh: fingerprint of a different key the server shows now"`
	NotifyURL      string `json:"notify_url,omitempty" jsonschema:"git: the address the Git host should call on a push (its secret is only shown in the web interface)"`
	NotifyProblem  string `json:"notify_problem,omitempty"`
	CheckedAt      string `json:"checked_at,omitempty" jsonschema:"git: when the panel last reached the repository"`
}

type siteSourceOutput struct {
	Site                string      `json:"site"`
	Source              *sourceInfo `json:"source" jsonschema:"null if the site is not linked to a repository"`
	PublishingAvailable bool        `json:"publishing_available" jsonschema:"false: this server does not build sites, so a push only records a run that waits"`
	LastRun             *runInfo    `json:"last_run" jsonschema:"the newest run of the site, null if there is none (list_site_runs lists more)"`
}

type runInfo struct {
	ID          string `json:"id"`
	Trigger     string `json:"trigger" jsonschema:"push: a push to the linked branch; manual: started with start_site_build (pusher names who)"`
	Status      string `json:"status" jsonschema:"queued, running, succeeded (built and made live), failed, cancelled or skipped (replaced by a newer run before it started)"`
	Repository  string `json:"repository"`
	Branch      string `json:"branch,omitempty"`
	Commit      string `json:"commit" jsonschema:"full commit id"`
	Message     string `json:"message" jsonschema:"first line of the commit message, as written by its author (untrusted text)"`
	AuthorName  string `json:"author_name"`
	AuthorLogin string `json:"author_login,omitempty"`
	Pusher      string `json:"pusher,omitempty"`
	Detail      string `json:"detail,omitempty" jsonschema:"why the run was skipped, failed or cancelled"`
	Waiting     string `json:"waiting,omitempty" jsonschema:"set while a queued run waits because this server does not build sites"`
	CreatedAt   string `json:"created_at"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Release     string `json:"release,omitempty" jsonschema:"the version a succeeded run published"`
}

func runOutput(r panel.Run) runInfo {
	stamp := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	return runInfo{
		ID: r.ID, Trigger: r.Trigger, Status: r.Status, Repository: r.Repository, Branch: r.Branch, Commit: r.Commit,
		Message: r.Message, AuthorName: r.AuthorName, AuthorLogin: r.AuthorLogin, Pusher: r.Pusher, Detail: r.Detail,
		Waiting: r.Waiting, CreatedAt: stamp(&r.CreatedAt), StartedAt: stamp(r.StartedAt), FinishedAt: stamp(r.FinishedAt),
		Release: r.Release,
	}
}

type siteRunsOutput struct {
	Site                string    `json:"site"`
	Runs                []runInfo `json:"runs" jsonschema:"newest first"`
	Kept                int       `json:"kept" jsonschema:"how many runs the panel keeps per site"`
	PublishingAvailable bool      `json:"publishing_available" jsonschema:"false: this server does not build sites, so queued runs wait"`
}

func sourceOutput(st panel.SiteSourceState) siteSourceOutput {
	out := siteSourceOutput{Site: st.Site, PublishingAvailable: st.PublishingAvailable}
	if st.LastRun != nil {
		r := runOutput(*st.LastRun)
		out.LastRun = &r
	}
	if s := st.Source; s != nil {
		out.Source = &sourceInfo{
			Kind: s.Kind, Repository: s.Repository, RepositoryID: s.RepositoryID, InstallationID: s.InstallationID,
			Account: s.Account, URL: s.URL, Branch: s.Branch, PublishOnPush: s.PublishOnPush, Status: s.Status,
			Problem: s.Problem, LinkedBy: s.LinkedBy, Server: s.Server, Transport: s.Transport, DeployKey: s.DeployKey,
			HostKey: s.HostKey, NewHostKey: s.NewHostKey, NotifyURL: s.NotifyURL, NotifyProblem: s.NotifyProblem,
		}
		if s.CheckedAt != nil {
			out.Source.CheckedAt = s.CheckedAt.UTC().Format(time.RFC3339)
		}
	}
	return out
}

type gitRepositoryInput struct {
	Site       string `json:"site" jsonschema:"site name as shown by list_sites"`
	Repository string `json:"repository" jsonschema:"SSH address like git@codeberg.org:owner/site.git or ssh://git@host:2222/owner/site.git, or HTTPS address of a public repository like https://codeberg.org/owner/site.git"`
}

type linkGitSourceInput struct {
	Site          string `json:"site" jsonschema:"site name as shown by list_sites"`
	Repository    string `json:"repository" jsonschema:"the address, as for inspect_git_repository"`
	Branch        string `json:"branch"`
	PublishOnPush bool   `json:"publish_on_push" jsonschema:"whether a push to the branch should publish the site"`
	HostKey       string `json:"host_key,omitempty" jsonschema:"for SSH: the fingerprint inspect_git_repository returned, after the person confirmed it matches what the Git host publishes"`
}

type gitCheckOutput struct {
	siteSourceOutput
	OK            bool     `json:"ok" jsonschema:"the repository was reached (with the deploy key over SSH)"`
	Branches      []string `json:"branches"`
	DefaultBranch string   `json:"default_branch,omitempty"`
	BranchFound   bool     `json:"branch_found" jsonschema:"the linked branch exists"`
}

type linkableRepositoryInfo struct {
	InstallationID int64  `json:"installation_id" jsonschema:"pass it to link_site_source"`
	RepositoryID   int64  `json:"repository_id" jsonschema:"pass it to link_site_source"`
	FullName       string `json:"full_name" jsonschema:"owner/name"`
	Account        string `json:"account"`
	Private        bool   `json:"private"`
	Archived       bool   `json:"archived"`
	DefaultBranch  string `json:"default_branch"`
}

type linkableRepositoriesOutput struct {
	Connected    bool                     `json:"connected" jsonschema:"a GitHub account is connected (only shown to administrators)"`
	Repositories []linkableRepositoryInfo `json:"repositories" jsonschema:"empty for anyone but administrators: the repositories belong to the administrators' accounts"`
	Problems     []string                 `json:"problems" jsonschema:"accounts whose repositories could not be listed"`
	Truncated    bool                     `json:"truncated"`
}

type sourceBranchesInput struct {
	Site           string `json:"site" jsonschema:"site name as shown by list_sites"`
	InstallationID int64  `json:"installation_id" jsonschema:"from list_site_source_repositories"`
	RepositoryID   int64  `json:"repository_id" jsonschema:"from list_site_source_repositories"`
}

type linkSourceInput struct {
	Site           string `json:"site" jsonschema:"site name as shown by list_sites"`
	InstallationID int64  `json:"installation_id" jsonschema:"from list_site_source_repositories"`
	RepositoryID   int64  `json:"repository_id" jsonschema:"from list_site_source_repositories"`
	Branch         string `json:"branch" jsonschema:"an existing branch of the repository, e.g. its default_branch"`
	PublishOnPush  bool   `json:"publish_on_push" jsonschema:"whether a push to the branch should publish the site"`
}

type unlinkSourceOutput struct {
	Unlinked string `json:"unlinked" jsonschema:"the site that is no longer linked"`
}

func reachesGitHub(a *sdk.ToolAnnotations) *sdk.ToolAnnotations {
	a.OpenWorldHint = ptr(true)
	return a
}

func addSourceTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "get_site_source",
		Title: "Show where a site's code comes from",
		Description: "Show the repository and branch a site is linked to and whether a push should publish it, or null " +
			"if it is not linked. publishing_available tells whether this server builds and publishes on push; " +
			"while it is false, linking only records the choice.",
		Annotations: reachesGitHub(annotations("Show where a site's code comes from", true, false, true)),
	}, func(ctx context.Context, c call, in siteInput) (siteSourceOutput, error) {
		st, err := p.GetSiteSource(ctx, c.actor, in.Site)
		return sourceOutput(st), err
	})

	add(s, &sdk.Tool{
		Name:  "list_site_source_repositories",
		Title: "List repositories a site can be linked to",
		Description: "List the GitHub repositories you may link the site to. Only administrators may link a site to a " +
			"repository of the connected GitHub accounts; for everyone else the list is empty. Accounts are connected " +
			"in the web interface under Settings → GitHub.",
		Annotations: reachesGitHub(annotations("List repositories a site can be linked to", true, false, true)),
	}, func(ctx context.Context, c call, in siteInput) (linkableRepositoriesOutput, error) {
		list, err := p.LinkableRepositories(ctx, c.actor, in.Site)
		if err != nil {
			return linkableRepositoriesOutput{}, err
		}
		out := linkableRepositoriesOutput{Connected: list.Connected, Repositories: []linkableRepositoryInfo{}, Problems: list.Problems, Truncated: list.Truncated}
		for _, r := range list.Repositories {
			out.Repositories = append(out.Repositories, linkableRepositoryInfo{
				InstallationID: r.InstallationID, RepositoryID: r.ID, FullName: r.FullName, Account: r.Account,
				Private: r.Private, Archived: r.Archived, DefaultBranch: r.DefaultBranch,
			})
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:        "list_site_source_branches",
		Title:       "List the branches of a repository",
		Description: "List the branches of a repository from list_site_source_repositories, to choose one for link_site_source.",
		Annotations: reachesGitHub(annotations("List the branches of a repository", true, false, true)),
	}, func(ctx context.Context, c call, in sourceBranchesInput) (panel.SourceBranches, error) {
		return p.SourceBranches(ctx, c.actor, in.Site, in.InstallationID, in.RepositoryID)
	})

	add(s, &sdk.Tool{
		Name:  "link_site_source",
		Title: "Link a site to a repository and branch",
		Description: "Link a site to a GitHub repository and branch, replacing the link it had; also used to change the " +
			"branch or publish_on_push. GitHub is asked whether the repository and the branch exist. Linking alone " +
			"publishes nothing: the next push (or start_site_build) builds the site with its build settings " +
			"(set_site_build_settings). Safe to repeat with the same values.",
		Annotations: reachesGitHub(annotations("Link a site to a repository and branch", false, true, true)),
	}, func(ctx context.Context, c call, in linkSourceInput) (siteSourceOutput, error) {
		st, err := p.SetSiteSource(ctx, c.actor, in.Site, panel.SiteSourceInput{
			InstallationID: in.InstallationID, RepositoryID: in.RepositoryID, Branch: in.Branch, PublishOnPush: in.PublishOnPush,
		})
		return sourceOutput(st), err
	})

	add(s, &sdk.Tool{
		Name:  "inspect_git_repository",
		Title: "Check the address of a Git repository",
		Description: "Check a Git repository's address before linking a site to it with link_git_site_source. For SSH " +
			"addresses it returns the fingerprint of the server's key: show it to the person and have them confirm it " +
			"matches what their Git host publishes before linking. For HTTPS (public repositories only) it lists the " +
			"branches. Only public servers are reached.",
		Annotations: reachesGitHub(annotations("Check the address of a Git repository", true, false, true)),
	}, func(ctx context.Context, c call, in gitRepositoryInput) (panel.GitRepositoryCheck, error) {
		return p.InspectGitRepository(ctx, c.actor, in.Site, in.Repository)
	})

	add(s, &sdk.Tool{
		Name:  "link_git_site_source",
		Title: "Link a site to a Git repository by address",
		Description: "Link a site to any Git repository (GitLab, Gitea, Forgejo, Codeberg, Bitbucket, …) by its address " +
			"and branch, replacing the link it had; also used to change the branch or publish_on_push. For SSH, pass the " +
			"confirmed host_key fingerprint from inspect_git_repository; the panel makes a deploy key for the site, " +
			"whose public half (deploy_key) the person adds to the repository with read access. Pushes are reported to " +
			"notify_url; its secret is only shown in the web interface, where the person makes one with \"New secret\". " +
			"Linking alone publishes nothing: see publishing_available.",
		Annotations: reachesGitHub(annotations("Link a site to a Git repository by address", false, true, true)),
	}, func(ctx context.Context, c call, in linkGitSourceInput) (siteSourceOutput, error) {
		st, err := p.SetGitSiteSource(ctx, c.actor, in.Site, panel.GitSourceInput{
			Repository: in.Repository, Branch: in.Branch, PublishOnPush: in.PublishOnPush, HostKey: in.HostKey,
		})
		return sourceOutput(st), err
	})

	add(s, &sdk.Tool{
		Name:  "check_git_site_source",
		Title: "Check access to a site's Git repository",
		Description: "Connect to the Git repository a site is linked to by address (with its deploy key over SSH) and list " +
			"its branches, to see whether the deploy key was added and the branch exists.",
		Annotations: reachesGitHub(annotations("Check access to a site's Git repository", false, false, true)),
	}, func(ctx context.Context, c call, in siteInput) (gitCheckOutput, error) {
		chk, err := p.CheckGitSiteSource(ctx, c.actor, in.Site)
		if err != nil {
			return gitCheckOutput{}, err
		}
		return gitCheckOutput{siteSourceOutput: sourceOutput(chk.SiteSourceState), OK: chk.OK, Branches: chk.Branches,
			DefaultBranch: chk.DefaultBranch, BranchFound: chk.BranchFound}, nil
	})

	add(s, &sdk.Tool{
		Name:  "unlink_site_source",
		Title: "Unlink a site from its repository",
		Description: "Remove the link between a site and its repository. The site and its published files stay as " +
			"they are; pushes no longer concern it.",
		Annotations: annotations("Unlink a site from its repository", false, true, false),
	}, func(ctx context.Context, c call, in siteInput) (unlinkSourceOutput, error) {
		if err := p.RemoveSiteSource(ctx, c.actor, in.Site); err != nil {
			return unlinkSourceOutput{}, err
		}
		return unlinkSourceOutput{Unlinked: in.Site}, nil
	})

	add(s, &sdk.Tool{
		Name:  "list_site_runs",
		Title: "List the runs of a site",
		Description: "List the newest runs of a site: one per push to its linked branch while publish_on_push is on, " +
			"and one per start_site_build, with commit, message, author, status and the published version. " +
			"read_site_run_log shows a run's build log. While publishing_available is false, queued runs wait.",
		Annotations: annotations("List the runs of a site", true, false, true),
	}, func(ctx context.Context, c call, in siteInput) (siteRunsOutput, error) {
		list, err := p.ListSiteRuns(ctx, c.actor, in.Site)
		if err != nil {
			return siteRunsOutput{}, err
		}
		out := siteRunsOutput{Site: list.Site, Runs: []runInfo{}, Kept: list.Kept, PublishingAvailable: list.PublishingAvailable}
		for _, r := range list.Runs {
			out.Runs = append(out.Runs, runOutput(r))
		}
		return out, nil
	})
}
