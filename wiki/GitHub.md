# GitHub

An administrator connects ownpanel to a GitHub account or organisation in a few clicks. Later steps use the connection to publish a site from a repository on every push ([#108](https://github.com/danielgtmn/ownpanel/issues/108), [#109](https://github.com/danielgtmn/ownpanel/issues/109), [#113](https://github.com/danielgtmn/ownpanel/issues/113)). The panel connects, shows the accounts and lists their repositories, a site can be linked to a repository and branch ([[Linking a site|Sites-and-Publishing#source-repository-and-branch]]), and GitHub tells the panel about every push. The panel records each push as a **run** of the site, builds the commit on the server with the site's [[build settings|Sites-and-Publishing#build-settings]] and makes the result live ([below](#what-happens-on-a-push)). Pull requests against the linked branch are built as **previews** that are not live, and the panel posts their address on the pull request ([below](#pull-requests)). On a server without the build environment ([[Building sites on the server|Sites-and-Publishing#building-a-site-from-its-source]]), runs wait.

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
- fetching the commit of a build, and looking at a repository's files to suggest build settings: read access to the contents of the one repository;
- reporting a build on its commit: write access to the commit statuses of the one repository (nothing else);
- reporting a preview on a pull request: write access to the commit statuses and the pull requests of the one repository (to read the pull request, find the panel's own comment and write it).

These tokens are never stored and never shown.

Asking for permissions now saves you from approving new ones on every installation later. If you prefer, you can lower them on GitHub (app settings → Permissions); the features that need them then fail with GitHub's message.

## What happens on a push

GitHub sends a notice to `https://<panel domain>/api/github/webhook` for every push to a repository the app may see. The panel checks it (see [[Security|Security#github]]) and then:

- **A push to the linked branch** of a site with **publish on push** on records a **run** for that site: the commit, its message (first line), its author, who pushed, and the time. Several sites linked to the same repository and branch each get their own run. Sites are found by GitHub's numeric ids of the repository and the installation, never by name.
- A run starts as **waiting** (`queued`). The panel builds one run per site at a time: it downloads the commit from GitHub, builds it in the build environment with the site's build settings, and publishes the output folder as a new version and makes it live, through the same checks and limits as an upload. A newer push replaces a run that has not started (it becomes **replaced by a newer build**, `skipped`); a run that is being built finishes first.
- **A failed, cancelled or timed-out build changes nothing**: the live version stays. So does a run of a site without build settings: it fails and asks for them.
- **Build now** on the site page (or the API and MCP) records a run of the newest commit of the linked branch, the same way, started by the person who pressed it.
- The **Builds** section on the site page lists the site's runs, newest first: the commit (its short id links to the commit on GitHub), its message, its author, what started it (a push or "Build now"), the status and the time. It looks for new runs every 20 seconds while the page is open, every 3 seconds while one waits or builds. **Details** opens one run with everything the panel knows about it: who pushed, the repository and branch, when it was received, started and finished, the version it published, why it was replaced or failed, and the **output** of the build, live while it builds. **Cancel** stops a run that waits or builds. The panel keeps the last 30 runs of each site, with their output (up to 1 MB each; beyond that, the beginning and the last 64 KB). The values of secret variables are hidden in it.
- **On GitHub**, each run shows as a status named `ownpanel` on its commit: pending while it builds, then success or failure with a short reason, and **Details** leads to the run on the site page (when the panel has its own domain). A status that cannot be posted (permission lowered on GitHub, GitHub unreachable) is only logged; the build is not affected.
- **When the panel restarts**, runs it was building are marked failed ("the panel restarted while the site was being built"), and their builds are removed from the build environment; waiting runs are built as usual. Stopping the panel does not cancel builds with a status of their own: they end with the panel.

  | Status | Meaning |
  |---|---|
  | Waiting | received, not built yet (on a server without the build environment: until it has one) |
  | Building | being built |
  | Published | built and made live |
  | Failed | the build did not finish; the details say why |
  | Cancelled | stopped before it finished |
  | Replaced by a newer build | a newer push or "Build now" came before this one was built |

**No run** is recorded for: pushes of tags, pushes that delete a branch, pushes to other branches, sites with publish on push off, sites whose connection is missing, whose repository the app no longer sees or whose access is paused on GitHub, and pushes older than seven days.

Other notices change what the site page shows:

| Notice | Effect |
|---|---|
| the app was uninstalled from an account | the account's installation is forgotten; its sites show **connection missing** |
| access paused / resumed on GitHub (suspend, unsuspend) | its sites show **access is paused** / linked again |
| a repository was taken away from the app / given back | sites linked to it show that the panel **no longer sees** it / linked again |
| a repository was renamed or transferred | the new name is shown |
| a repository was deleted | sites linked to it show that the panel no longer sees it |

| a repository was made public | the previews of its pull requests are removed from every site (their links were posted while it was private, see [below](#who-sees-the-preview-link)) |

Pull requests are described [below](#pull-requests). Everything else (the test notice GitHub sends when the app is created, for example) is accepted and ignored.

GitHub does not send a notice again by itself when the panel could not be reached. On GitHub, the app's **Advanced → Recent Deliveries** lists the last notices and can **Redeliver** one: the panel handles it if it had not accepted it before, and ignores it if it had.

## Pull requests

A pull request against the linked branch of a site is built as a **preview**: a version that is not live, with its own [[preview address|Sites-and-Publishing#previews]], posted on the pull request ([[Previews of pull requests|Sites-and-Publishing#previews-of-pull-requests]]). It is on for every linked site unless switched off: **Pull requests** in the Source section of the site page, `PUT /api/sites/{name}/source/previews` or MCP `set_site_pull_request_previews`.

- **Opened, reopened or a new commit pushed to it** (GitHub's `opened`, `reopened` and `synchronize`): the panel records a run of the pull request's newest commit for every site linked to the repository (through the same installation) whose branch is the pull request's base branch and has previews of pull requests on. Its trigger is **Pull request #n**, its message is the pull request's title. Other changes to a pull request (title, labels, its base branch, …) start nothing; a pull request whose base branch is changed to the linked branch is built with its next commit.
- The runner builds it like a push, with the site's build settings, but publishes the output as a version that is **not live**. A newer commit replaces a run of the same pull request that has not started; runs of a pull request never replace the site's other runs or those of other pull requests. Pushes and Build now go first, then pull requests in the order they came.
- **Closed or merged**: queued and running runs of the pull request are cancelled ("the pull request was closed"), and its previews are removed from every site. A preview that someone made live is a normal version from then on and stays.
- **On GitHub**: a commit status named `ownpanel preview` on the pull request's commit (pending while it builds, then success or failure), and **one comment** of the panel per site, written when the first build ends and updated after every later one and when the pull request is closed. The panel finds its comment again by a hidden marker *and* by its author (the app's own bot): a comment someone else writes with the same marker is left alone. A failed build's comment says so and links to the run in the panel, without the reason (it stays in the panel). Cancelled runs leave the comment as it was. Failures to post are only logged.
- In the **Builds** section, runs of pull requests are marked **Pull request #n** (a link to it on GitHub), say **Preview ready** instead of Published, and have **Open preview** while their preview exists. **Details** shows the title, where the pull request comes from and the preview's link.

### Who sees the preview link

The preview link carries the key that opens the preview ([[Previews|Sites-and-Publishing#previews]]). Comments and commit statuses of a **public** repository are public, and so are GitHub's notification e-mails about them. The panel therefore decides each time it posts, asking GitHub whether the pull request's repository is private (when GitHub cannot say, it counts as public):

- **Private repository:** the comment and the status's **Details** carry the full preview link. Everyone who can read the pull request can open the preview, as long as it exists.
- **Public repository:** the comment and the status link to the run in the panel instead. Whoever may see the site there finds **Open preview**; nobody else learns the key.
- A repository that is **made public** loses the previews of all its pull requests at once, so links posted while it was private stop working. The comments themselves stay on GitHub (with a link that answers "Unknown preview."); new pushes to the pull requests build new previews, posted the public way.

### Pull requests from forks

A pull request from a fork runs code from someone outside the repository. It is **not built** unless the site has **Also from forks** switched on (off by default; it needs previews of pull requests on). When it is on, a fork's pull request is built:

- **without any of the site's variables**, plain or secret;
- with an **empty cache of its own**, removed after the build, never the site's cache: nothing a fork's build leaves behind reaches a later build of the site (which gets its secrets);
- in the same isolated build environment as every build ([[Security|Security#builds]]);
- **marked** as coming from a fork in the panel ("Pull request #n from a fork", with the fork's name) and in its comment on GitHub.

A fork whose repository was deleted still counts as a fork. Whether a pull request is a fork's is decided by GitHub's ids of the two repositories, never by names.

## Who may use it

- **Administrators** connect and remove accounts, see them and their repositories, and link sites to their repositories.
- **Members** do not see the connections, and cannot list or use their repositories. To them a repository of a connection looks like one that does not exist. The repositories belong to the administrators' accounts and organisations, and a member could otherwise publish (and so expose) any of them on a domain of their own.

Members who want to publish from their own repositories use the [[GitHub Action|GitHub-Action]] or, later, deploy keys ([#111](https://github.com/danielgtmn/ownpanel/issues/111)).

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
- Previews of pull requests: `PUT /api/sites/{name}/source/previews` and `set_site_pull_request_previews`.
- Runs: `GET /api/sites/{name}/runs` and the MCP tool `list_site_runs` (read only); the site source also carries the newest run as `last_run`. Build now: `POST /api/sites/{name}/runs` and `start_site_build`; cancel: `POST /api/sites/{name}/runs/{id}/cancel` and `cancel_site_run`; the output: `GET /api/sites/{name}/runs/{id}/log` and `read_site_run_log`.
- Build settings: `/api/sites/{name}/build-settings` and `get_site_build_settings`, `set_site_build_settings`, `propose_site_build_settings`.

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

To build something, first give the stand-in a commit: post a folder as a tar.gz, and the push then names it:

```sh
tar -czf - -C my-site . | curl --data-binary @- 'http://127.0.0.1:9101/_dev/commit?repo=acme/website&branch=main'
```

Pull requests work the same way: give the head branch a commit, then open, update and close a pull request (the script prints its number; `stranger/website` is a fork no installation sees):

```sh
tar -czf - -C my-change . | curl --data-binary @- 'http://127.0.0.1:9101/_dev/commit?repo=acme/website&branch=dev'
scripts/github-pull.sh http://127.0.0.1:9101 https://panel.localhost:8443 acme/website opened dev main
scripts/github-pull.sh http://127.0.0.1:9101 https://panel.localhost:8443 acme/website synchronize 1
scripts/github-pull.sh http://127.0.0.1:9101 https://panel.localhost:8443 acme/website closed 1
curl -s 'http://127.0.0.1:9101/_dev/comments?repo=acme/website&number=1'
```
