package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/danielgtmn/ownpanel/internal/panel"
)

// GitHub: read only. Connecting an account and removing a connection
// happen in the web interface; creating the GitHub App needs the person to
// confirm on GitHub in their browser.

type githubInstallationInfo struct {
	InstallationID      int64  `json:"installation_id" jsonschema:"id to list its repositories with list_github_repositories"`
	Account             string `json:"account" jsonschema:"the GitHub user or organisation it is installed on"`
	AccountType         string `json:"account_type" jsonschema:"user or organization"`
	RepositorySelection string `json:"repository_selection" jsonschema:"all: every repository of the account; selected: only the ones chosen on GitHub"`
	RepositoryCount     *int   `json:"repository_count" jsonschema:"how many repositories it may see, if listed before"`
	Suspended           bool   `json:"suspended" jsonschema:"suspended on GitHub, so it cannot be used"`
}

type githubConnectionInfo struct {
	ID            string                   `json:"id" jsonschema:"the panel's id of the connection"`
	Name          string                   `json:"name" jsonschema:"name of the GitHub App the panel created"`
	Owner         string                   `json:"owner" jsonschema:"GitHub user or organisation that owns the App"`
	OwnerType     string                   `json:"owner_type" jsonschema:"user or organization"`
	Problem       string                   `json:"problem,omitempty" jsonschema:"what went wrong the last time GitHub was asked, if anything"`
	Installations []githubInstallationInfo `json:"installations" jsonschema:"accounts the App is installed on; empty until repositories are chosen in the web interface"`
}

type githubStatusOutput struct {
	Connected   bool                   `json:"connected" jsonschema:"at least one GitHub account is connected"`
	CanConnect  bool                   `json:"can_connect" jsonschema:"the panel has a public address with HTTPS, which connecting needs"`
	Problem     string                 `json:"problem,omitempty" jsonschema:"why the panel cannot be connected yet"`
	Connections []githubConnectionInfo `json:"connections"`
}

type githubRepositoriesInput struct {
	InstallationID int64 `json:"installation_id" jsonschema:"installation_id from get_github_status"`
}

type githubRepositoryInfo struct {
	ID            int64  `json:"id" jsonschema:"GitHub's id of the repository"`
	FullName      string `json:"full_name" jsonschema:"owner/name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	URL           string `json:"url" jsonschema:"the repository on GitHub"`
}

type githubRepositoriesOutput struct {
	Repositories []githubRepositoryInfo `json:"repositories"`
	Truncated    bool                   `json:"truncated" jsonschema:"there are more repositories than listed"`
}

// addGitHubTools registers the read-only GitHub tools.
func addGitHubTools(s *sdk.Server, p *panel.Service) {
	add(s, &sdk.Tool{
		Name:  "get_github_status",
		Title: "Show the GitHub connections",
		Description: "Show whether ownpanel is connected to GitHub: the connected accounts and organisations, where the " +
			"panel's GitHub App is installed and how many repositories it may see. Only administrators can see it. " +
			"Connecting or removing an account cannot be done with these tools: an administrator does it in the web " +
			"interface under Settings → GitHub, because GitHub asks for a confirmation in the browser.",
		Annotations: annotations("Show the GitHub connections", true, false, true),
	}, func(ctx context.Context, c call, _ noInput) (githubStatusOutput, error) {
		gh, err := p.GitHubConnections(ctx, c.actor)
		if err != nil {
			return githubStatusOutput{}, err
		}
		out := githubStatusOutput{Connected: len(gh.Apps) > 0, CanConnect: gh.Ready, Problem: gh.Problem, Connections: []githubConnectionInfo{}}
		for _, a := range gh.Apps {
			conn := githubConnectionInfo{ID: a.ID, Name: a.Name, Owner: a.Owner, OwnerType: a.OwnerType, Problem: a.LastError,
				Installations: []githubInstallationInfo{}}
			for _, i := range a.Installations {
				conn.Installations = append(conn.Installations, githubInstallationInfo{
					InstallationID: i.ID, Account: i.Account, AccountType: i.AccountType,
					RepositorySelection: i.RepositorySelection, RepositoryCount: i.RepositoryCount, Suspended: i.Suspended,
				})
			}
			out.Connections = append(out.Connections, conn)
		}
		return out, nil
	})

	add(s, &sdk.Tool{
		Name:  "list_github_repositories",
		Title: "List GitHub repositories",
		Description: "List the repositories an installation of the panel's GitHub App may see (installation_id from " +
			"get_github_status). Only administrators can list them. Which repositories it may see is chosen on GitHub, " +
			"from the web interface under Settings → GitHub.",
		Annotations: annotations("List GitHub repositories", true, false, true),
	}, func(ctx context.Context, c call, in githubRepositoriesInput) (githubRepositoriesOutput, error) {
		list, err := p.GitHubRepositories(ctx, c.actor, in.InstallationID)
		if err != nil {
			return githubRepositoriesOutput{}, err
		}
		out := githubRepositoriesOutput{Repositories: []githubRepositoryInfo{}, Truncated: list.Truncated}
		for _, r := range list.Repositories {
			out.Repositories = append(out.Repositories, githubRepositoryInfo{
				ID: r.ID, FullName: r.FullName, Private: r.Private, Archived: r.Archived, DefaultBranch: r.DefaultBranch, URL: r.HTMLURL,
			})
		}
		return out, nil
	})
}
