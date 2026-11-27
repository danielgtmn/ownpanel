# Git repositories

A site can take its code from **any Git repository**: GitLab, Codeberg, Forgejo, Gitea, Bitbucket, a server of your own, or a GitHub repository where the [[GitHub connection|GitHub]] cannot be installed. The panel makes a **deploy key** for the site (a key that may only read that repository) and a **notification address** the Git host calls on every push. Unlike a GitHub connection, this works for **members** too: the repository is your own, and you add the key yourself.

Pushes are built and published like pushes through a GitHub connection: the panel fetches the commit, builds it with the site's build settings and makes the result live ([[Sites and Publishing|Sites-and-Publishing#build-settings]]). **Build now** and proposing build settings work the same way. On a server without the build environment, runs wait and the answers of the API and MCP say `"publishing_available": false`.

## Linking a site

On the site page, **Source → Link repository** (or **Change**), then **Other Git host**:

1. **Enter the address** you would clone the repository with:
   - `git@codeberg.org:owner/site.git` or `ssh://git@git.example.org:2222/owner/site.git` for a private (or any) repository, read with the deploy key;
   - `https://codeberg.org/owner/site.git` for a **public** repository, read without a key. The panel never sends a password or token over HTTPS.
2. **Check the server key.** For an SSH address the panel connects to the server and shows the fingerprint of its key, like `SHA256:mb8qW…`. Compare it with the fingerprint your Git host publishes (in its documentation or settings) and link only if they match: from then on the panel talks only to a server showing exactly this key. For an HTTPS address the panel lists the branches instead.
3. **Choose the branch** and whether a push publishes the site, and link.
4. **Add the deploy key** the Source section shows to the repository, with **read access only**:
   - GitHub: the repository's Settings → Deploy keys → Add deploy key (leave "Allow write access" off);
   - GitLab: Settings → Repository → Deploy keys (leave "Grant write permissions" off);
   - Codeberg, Forgejo, Gitea: Settings → Deploy keys;
   - Bitbucket: Repository settings → Access keys.
5. **Add the notification address.** Right after linking, the panel shows the address and its **secret once**. At the Git host add a webhook with that address and secret, content type `application/json`, for push events (GitLab: the secret goes into "Secret token"). If you lose the secret, make a new address (below).
6. **Check access.** The panel connects with the deploy key, lists the branches and tells you whether the branch exists.

Changing the branch or "publish on push" later keeps the deploy key and the notification address, so nothing needs to change at the Git host. Linking another repository on the same server keeps the confirmed server key; another server asks for its key again.

## On the site page

The Source section shows, for a repository linked by address:

- the server, the branch, whether a push publishes, and who linked it;
- the **deploy key** to copy, and **Make a new key**: the old key stops working for the site at once; add the new one at the Git host and remove the old one there;
- the **server key** the panel trusts; if the server shows another one, both fingerprints and **Trust the new key** (below);
- the **notification address** to copy, and **New address and secret**: the old address stops working at once, the new secret is shown once;
- **Check access** and when the panel last tried to reach the repository.

Status (`status` in the API and MCP): `linked`; `not_checked` (not reached since linking or since a new deploy key: add the key, then check access); `access_failed` (the last attempt failed, `problem` says why, for example that the deploy key was not added); `host_key_changed` (below).

## When the server key changes

If the server shows a different key than the one confirmed for the site, the panel **refuses to connect** before it sends anything, not even the deploy key's signature, and the site shows `host_key_changed` with the old and the new fingerprint. Nothing is fetched until someone who may change the site checks the new fingerprint with the Git host and clicks **Trust the new key**. The panel trusts it only if the server shows exactly the fingerprint that was confirmed. Servers do change their keys now and then (Git hosts announce it); a change nobody announced can mean someone is pretending to be the server.

## What happens on a push

The Git host calls the notification address. The panel checks the secret in whichever form the host sends it and the delivery id (see [[Security|Security#git-repositories]]), answers at once, and then, for a **push to the linked branch** while "publish on push" is on, asks the repository where the branch points now and records a **run** of that commit, in the same list of runs as for GitHub ([[GitHub|GitHub#what-happens-on-a-push]]), with the message (first line), author and pusher from the notification when it names that commit. A newer push replaces a run that has not started; a notification for a commit that already waits, is being built or was published adds nothing. Tags, other branches, deleted branches and other events (a test notification, merge requests) are accepted and ignored. The answer is `{"result": "accepted"}`; the run appears a moment later.

| Host | How it proves the notification |
|---|---|
| GitHub | `X-Hub-Signature-256`: HMAC-SHA256 of the body with the secret |
| Gitea, Forgejo (Codeberg) | `X-Gitea-Signature`, `X-Forgejo-Signature` (and `X-Hub-Signature-256`) |
| Gogs | `X-Gogs-Signature` |
| GitLab | `X-Gitlab-Token`: the secret itself (GitLab signs nothing) |
| Bitbucket Cloud and Data Center | `X-Hub-Signature` with `sha256=` |

The commit a notification names is never built: the panel finds the commit itself, and when the run is built it fetches the commit the branch points at **then** and fails the run if the branch moved on (a newer push has its own run). Notifications without a delivery id are refused, so one cannot be sent again unnoticed.

Builds do not depend on notifications: **Build now** asks the repository for the branch with the deploy key, so it works without a notification address.

## Who may use it

- **Seeing** a source linked by address (address, branch, public deploy key, server key fingerprints, notification address) is for whoever may see the site, read-only tokens included.
- **Linking, checking access, trusting a new server key, making a new key or address** need the right to change the site: its owner or an administrator, a `write` token, a token limited to sites only for its sites. Read-only tokens get `403`, sites you may not see `404`.
- The private half of the deploy key and the notification secret are never returned, not even to administrators; the secret is shown once, in the answer that made it. MCP never shows it: after linking through MCP, make a new address in the web interface.
- Checks that connect to Git servers are limited per person (bursts of 20, then one every 5 seconds).

## API and MCP

- HTTP: `POST /api/sites/{name}/source/git/inspect`, `PUT /api/sites/{name}/source/git`, `POST …/source/git/check`, `…/host-key`, `…/deploy-key`, `…/notify-secret`, and the notification address `POST /api/git/notify/{id}` ([[API|API#sites]]).
- MCP: `inspect_git_repository` (read-only), `link_git_site_source`, `check_git_site_source`; `get_site_source` and `list_site_runs` show sources and runs of both kinds ([[MCP|MCP#site-sources-and-runs]]). Trusting a new server key and making new keys or addresses are done in the web interface or the API.

## Security

See [[Security|Security#git-repositories]]: how the address, the server and what it sends are treated, what is stored, and what is not protected.

## Testing locally

Repository addresses must point at public servers, so a Git server in a container on your own machine is refused. For development only, `-git-private-hosts` (accepted only with `-tls internal`) lets addresses point at loopback and private networks, for example a Gitea or Forgejo container whose SSH server listens on `127.0.0.1:2222`:

```sh
bin/ownpaneld … -tls internal -panel-domain panel.localhost -git-private-hosts
```

Never use it on a real server: anyone who may link a site could then make the panel connect into the server's own network.
