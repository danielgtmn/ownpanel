# GitHub

An administrator connects ownpanel to a GitHub account or organisation in a few clicks. Later steps use the connection to publish a site from a repository on every push ([#108](https://github.com/danielgtmn/ownpanel/issues/108), [#109](https://github.com/danielgtmn/ownpanel/issues/109), [#113](https://github.com/danielgtmn/ownpanel/issues/113)). Today the panel connects, shows the accounts and lists their repositories, a site can be linked to a repository and branch ([[Linking a site|Sites-and-Publishing#source-repository-and-branch]]), and GitHub tells the panel about every push, which the panel records as a **run** of the site and lists on the site page ([below](#what-happens-on-a-push)). Building and publishing is not available yet: a run waits until building on the server is available ([#113](https://github.com/danielgtmn/ownpanel/issues/113)).

To publish from GitHub without connecting anything, use the [[GitHub Action|GitHub-Action]]: it builds in GitHub's CI and uploads the result.

## How it works

The panel creates **its own GitHub App**, the way Coolify and Dokploy do. There is no central ownpanel app: every installation of ownpanel has its own, and only that server holds its keys.

1. **Settings → GitHub → Connect GitHub.** Choose your own GitHub account or an organisation (you must be one of its owners).
2. The panel sends you to GitHub with a description of the app (its name, the panel's addresses and the permissions below). You check it and click **Create GitHub App**.
3. GitHub sends you back. The panel receives the app's private key, its secret for signing deliveries and its client secret, and stores them encrypted.
4. **Choose repositories.** GitHub asks where to install the app and which repositories it may see: all of the account's or chosen ones. You come back to the panel, which shows the account and how many repositories it can see.

A private GitHub App can only be installed on the account that owns it. So **every account or organisation gets its own app**: "Add another account" repeats the steps for the next one. A public app would avoid that, but anyone on GitHub could then install it and send their repositories into your panel.

Each account in **Settings → GitHub** shows how many repositories the panel may see and how many sites are linked to them. **Show repositories** lists them, with whether each is private or archived, its default branch and the sites linked to it (with their branch); sites can be linked to any of them. **Change on GitHub** next to an account opens GitHub's page where you choose other repositories; GitHub then sends you back and the panel updates. **Check again** asks GitHub afresh where the app is installed and how many repositories it may see (for example after changes made directly on GitHub). The list of repositories is kept for a minute.

### Requirements

- **The panel needs its own domain with HTTPS** ([[Configuration|Configuration]], Settings → Panel address). GitHub sends your browser back to `https://<panel domain>/…` and will send its notices about pushes there. Without a domain, or on a server without certificates (`-tls off`), connecting is refused with that explanation.
- The domain must be reachable from the internet for the notices. The connection itself only needs your browser to reach it.
- Connecting needs an administrator signed in with a password; GitHub asks you to confirm in the browser, so it cannot be done with an access token or over MCP.

## What the app may do

The app asks for exactly what the planned steps need, nothing more:

| Permission | Access | Why |
|---|---|---|
| Repository contents | read | fetch a commit to build it ([#113](https://github.com/danielgtmn/ownpanel/issues/113)) and list branches ([#108](https://github.com/danielgtmn/ownpanel/issues/108)) |
| Metadata | read | required for every app: names and settings of repositories |
| Commit statuses | read and write | report a build's result on the commit ([#113](https://github.com/danielgtmn/ownpanel/issues/113)) |
| Pull requests | read and write | post the preview address on a pull request ([#116](https://github.com/danielgtmn/ownpanel/issues/116)) |

It subscribes to **push**, **repository** (renamed, transferred, deleted) and **pull request** notices. GitHub always sends notices about the app's own installation. Apps connected before the repository notices were added only send pushes and pull requests; add **Repository** under the app's settings on GitHub (Permissions & events → Subscribe to events) to get renames at once, otherwise the name is refreshed on the next push or when the site page asks GitHub. The app **cannot change code**: it has no write access to contents.

For each use the panel asks GitHub for a short-lived token (at most an hour) narrowed to what that use needs:

- listing repositories: metadata read only;
- listing a repository's branches and checking the branch when a site is linked: read access to the contents of that one repository;
- fetching code (later, for builds): read access to the contents of the one repository being built.

These tokens are never stored and never shown.

Asking for permissions now saves you from approving new ones on every installation later. If you prefer, you can lower them on GitHub (app settings → Permissions); the features that need them then fail with GitHub's message.

## What happens on a push

GitHub sends a notice to `https://<panel domain>/api/github/webhook` for every push to a repository the app may see. The panel checks it (see [[Security|Security#github]]) and then:

- **A push to the linked branch** of a site with **publish on push** on records a **run** for that site: the commit, its message (first line), its author, who pushed, and the time. Several sites linked to the same repository and branch each get their own run. Sites are found by GitHub's numeric ids of the repository and the installation, never by name.
- A run starts as **waiting** (`queued`). Today it stays that way: building on the server is not there yet ([#113](https://github.com/danielgtmn/ownpanel/issues/113)), and the site page and the API say so. A newer push replaces a run that has not started (it becomes **replaced by a newer push**, `skipped`).
- The **Builds** section on the site page lists the site's runs, newest first: the commit (its short id links to the commit on GitHub), its message, its author, what started it (a push), the status and the time. It looks for new pushes every 20 seconds while the page is open. **Details** opens one run with everything the panel knows about it: who pushed, the repository and branch, when it was received, started and finished, and why it waits, was replaced or failed. The output of the build will be shown there too once builds on the server exist ([#115](https://github.com/danielgtmn/ownpanel/issues/115)). The panel keeps the last 30 runs of each site.

  | Status | Meaning |
  |---|---|
  | Waiting | received, not built yet (today: until building on the server is available) |
  | Building | being built |
  | Published | built and made live |
  | Failed | the build did not finish; the details say why |
  | Cancelled | stopped before it finished |
  | Replaced by a newer push | a newer push came before this one was built |

**No run** is recorded for: pushes of tags, pushes that delete a branch, pushes to other branches, sites with publish on push off, sites whose connection is missing, whose repository the app no longer sees or whose access is paused on GitHub, and pushes older than seven days.

Other notices change what the site page shows:

| Notice | Effect |
|---|---|
| the app was uninstalled from an account | the account's installation is forgotten; its sites show **connection missing** |
| access paused / resumed on GitHub (suspend, unsuspend) | its sites show **access is paused** / linked again |
| a repository was taken away from the app / given back | sites linked to it show that the panel **no longer sees** it / linked again |
| a repository was renamed or transferred | the new name is shown |
| a repository was deleted | sites linked to it show that the panel no longer sees it |

Everything else (pull requests for now, [#116](https://github.com/danielgtmn/ownpanel/issues/116); the test notice GitHub sends when the app is created) is accepted and ignored.

GitHub does not send a notice again by itself when the panel could not be reached. On GitHub, the app's **Advanced → Recent Deliveries** lists the last notices and can **Redeliver** one: the panel handles it if it had not accepted it before, and ignores it if it had.

## Who may use it

- **Administrators** connect and remove accounts, see them and their repositories, and link sites to their repositories.
- **Members** do not see the connections, and cannot list or use their repositories. To them a repository of a connection looks like one that does not exist. The repositories belong to the administrators' accounts and organisations, and a member could otherwise publish (and so expose) any of them on a domain of their own.

Members who want to publish from their own repositories use the [[GitHub Action|GitHub-Action]] or link the repository by its address with a deploy key ([[Git repositories|Git-Repositories]]), which also works for GitHub repositories.

## Removing a connection

**Remove** forgets the app's private key and secrets and its installations. The panel can no longer use the app. Sites keep their published versions. Sites linked to one of its repositories keep the link, marked **connection missing** on the site page (status `connection_missing` in the API): the repository and branch stay visible, nothing is published from them, and an administrator links the site again (after connecting the account again) or unlinks it. **Settings → GitHub** lists such sites under the accounts, with links to their pages. The same happens when the app is uninstalled on GitHub and **Check again** notices it.

The app itself stays on GitHub until you delete it there: the panel shows a link to its settings page, where **Advanced → Delete GitHub App** removes it and all its installations. Do that too; an app whose key exists nowhere any more is harmless but clutters your account.

If someone deletes the app on GitHub first, the panel shows that GitHub no longer accepts the connection; remove it in the panel as well.

## Security

See [[Security|Security#github]]: what is stored, how the return from GitHub is checked, and what someone with a copy of the database could do.

## API and MCP

- HTTP: `/api/github/…` ([[API|API#github]]).
- MCP: `get_github_status` and `list_github_repositories`, read only ([[MCP|MCP]]). Connecting and removing are done in the web interface.
- Linking a site: `/api/sites/{name}/source` and the MCP tools `get_site_source`, `list_site_source_repositories`, `list_site_source_branches`, `link_site_source`, `unlink_site_source`.
- Runs: `GET /api/sites/{name}/runs` and the MCP tool `list_site_runs` (read only); the site source also carries the newest run as `last_run`.

## Testing without GitHub

For development, `-github-url` points the panel at a stand-in instead of github.com (http is allowed only on a loopback address):

```sh
go run ./internal/github/githubtest/fakegithub -listen 127.0.0.1:9101
bin/ownpaneld … -panel-domain panel.localhost -tls internal -github-url http://127.0.0.1:9101
```

The stand-in confirms the app with one click and installs it on all repositories of the account at once. Its repositories have a few branches (`acme/website` has `main`, `release/2.0` and `dev`) to try linking a site. It is not part of the panel's binary.

To see what a push does, let the stand-in sign one with the app's secret and send it to the panel (run it twice with the same delivery id to see the second one ignored):

```sh
scripts/github-push.sh http://127.0.0.1:9101 https://panel.localhost:8443 acme/website main "Fix the header" my-delivery-1
```
