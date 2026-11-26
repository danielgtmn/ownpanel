# GitHub

An administrator connects ownpanel to a GitHub account or organisation in a few clicks. Later steps use the connection to publish a site from a repository on every push ([#108](https://github.com/danielgtmn/ownpanel/issues/108), [#109](https://github.com/danielgtmn/ownpanel/issues/109), [#113](https://github.com/danielgtmn/ownpanel/issues/113)). Today the panel connects, shows the accounts and lists their repositories. Linking a site to a repository is not built yet.

To publish from GitHub without connecting anything, use the [[GitHub Action|GitHub-Action]]: it builds in GitHub's CI and uploads the result.

## How it works

The panel creates **its own GitHub App**, the way Coolify and Dokploy do. There is no central ownpanel app: every installation of ownpanel has its own, and only that server holds its keys.

1. **Settings → GitHub → Connect GitHub.** Choose your own GitHub account or an organisation (you must be one of its owners).
2. The panel sends you to GitHub with a description of the app (its name, the panel's addresses and the permissions below). You check it and click **Create GitHub App**.
3. GitHub sends you back. The panel receives the app's private key, its secret for signing deliveries and its client secret, and stores them encrypted.
4. **Choose repositories.** GitHub asks where to install the app and which repositories it may see: all of the account's or chosen ones. You come back to the panel, which shows the account and how many repositories it can see.

A private GitHub App can only be installed on the account that owns it. So **every account or organisation gets its own app**: "Add another account" repeats the steps for the next one. A public app would avoid that, but anyone on GitHub could then install it and send their repositories into your panel.

**Change on GitHub** next to an account opens GitHub's page where you choose other repositories; GitHub then sends you back and the panel updates. **Check again** asks GitHub afresh where the app is installed and how many repositories it may see (for example after changes made directly on GitHub). The list of repositories is kept for a minute.

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

It subscribes to **push** and **pull request** notices. GitHub always sends notices about the app's own installation. The app **cannot change code**: it has no write access to contents.

For each use the panel asks GitHub for a short-lived token (at most an hour) narrowed to what that use needs:

- listing repositories: metadata read only;
- fetching code (later, for builds): read access to the contents of the one repository being built.

These tokens are never stored and never shown.

Asking for permissions now saves you from approving new ones on every installation later. If you prefer, you can lower them on GitHub (app settings → Permissions); the features that need them then fail with GitHub's message.

## Who may use it

- **Administrators** connect and remove accounts, see them and their repositories, and (once [#108](https://github.com/danielgtmn/ownpanel/issues/108) exists) link sites to their repositories.
- **Members** do not see the connections, and cannot list or use their repositories. To them a repository of a connection looks like one that does not exist. The repositories belong to the administrators' accounts and organisations, and a member could otherwise publish (and so expose) any of them on a domain of their own.

Members who want to publish from their own repositories use the [[GitHub Action|GitHub-Action]] or, later, deploy keys ([#111](https://github.com/danielgtmn/ownpanel/issues/111)).

## Removing a connection

**Remove** forgets the app's private key and secrets and its installations. The panel can no longer use the app. Sites keep their published versions; once sites can be linked ([#108](https://github.com/danielgtmn/ownpanel/issues/108)), the links to the removed account stop publishing.

The app itself stays on GitHub until you delete it there: the panel shows a link to its settings page, where **Advanced → Delete GitHub App** removes it and all its installations. Do that too; an app whose key exists nowhere any more is harmless but clutters your account.

If someone deletes the app on GitHub first, the panel shows that GitHub no longer accepts the connection; remove it in the panel as well.

## Security

See [[Security|Security#github]]: what is stored, how the return from GitHub is checked, and what someone with a copy of the database could do.

## API and MCP

- HTTP: `/api/github/…` ([[API|API#github]]).
- MCP: `get_github_status` and `list_github_repositories`, read only ([[MCP|MCP]]). Connecting and removing are done in the web interface.

## Testing without GitHub

For development, `-github-url` points the panel at a stand-in instead of github.com (http is allowed only on a loopback address):

```sh
go run ./internal/github/githubtest/fakegithub -listen 127.0.0.1:9101
bin/ownpaneld … -panel-domain panel.localhost -tls internal -github-url http://127.0.0.1:9101
```

The stand-in confirms the app with one click and installs it on all repositories of the account at once. It is not part of the panel's binary.
