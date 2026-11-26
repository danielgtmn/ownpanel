# Sites and Publishing

A **site** is a set of static files (HTML, CSS, JavaScript, images) served on one or more domains with automatic HTTPS.

## Sites

| | Rule |
|---|---|
| Name | Lowercase letters, digits and hyphens, 1 to 63 characters, starting and ending with a letter or digit. The name identifies the site in the API, in MCP and on disk; it is not a domain and cannot be changed. |
| Owner | The user who created the site. Members see and manage only their own sites; admins see all. |
| Domains | A list, possibly empty. Stored in lowercase, duplicates removed. Each must be a plain host name with at least one dot (no wildcards, no ports, no paths). |

A domain can belong to only one site. The panel's own domain (`-panel-domain`) cannot be used by a site. If a domain is taken by a site you cannot see, the error says only that it is used by another site. Members also cannot use another person's mail domain or its `autoconfig.`/`autodiscover.` names (where mail programs look for their settings); admins can.

Creating a site and changing its domains is one operation: `PUT /api/sites/{name}` with `{"domains": [...]}` creates the site if the name is free (you become the owner) and replaces the domain list if you may already see it. In MCP these are `create_site` and `set_site_domains`.

Changing domains replaces the whole list: domains not listed stop being served.

### What visitors see

| Situation | Answer |
|---|---|
| Site has a live version | the files, compressed with zstd or gzip |
| Site has no version yet | `503` with "This site has no content yet." |
| Site has no domains | not served |
| Host that no site claims | `404` with "Unknown site." over plain HTTP. Over HTTPS there is no certificate for such a name, so the connection fails before any answer. |
| Site whose only versions were published without making them live | `503` with "This site has no content yet.", like a site without a version |
| Preview address of a kept version, opened with its link | that version's files, with `X-Robots-Tag: noindex, nofollow` (see [Previews](#previews)) |

For HTTPS the domain must point at the server so a certificate can be issued. With `-tls acme` certificates are requested automatically; HTTP requests are redirected to HTTPS. See [[DNS|DNS]] for checking and fixing where a domain points.

## Archives

A version is created from an archive:

- **Formats:** `.zip` or `.tar.gz` (the web interface also accepts `.tgz`). The format is detected from the file's content, not its name.
- **Web root:** the archive's root is the site's root. If the archive contains exactly one top-level folder, that folder is unwrapped, so zipping the folder instead of its contents works.
- **Ignored:** macOS packaging files (`__MACOSX`, `.DS_Store`, `._*`).
- **Refused** (the upload fails with `400` and nothing changes): links (symbolic or hard) and other non-regular entries, entry names outside the site (`..`, absolute paths), names with backslashes, password-protected zip entries, archives without any file, damaged archives.

### Limits

| Limit | Value |
|---|---|
| Upload size | 512 MB |
| Unpacked size | 1 GiB |
| Entries (files and folders) | 100,000 |
| Versions kept per site | 5 |
| Upload time | 1 h; cut off earlier only if no data arrives for 1 min (see [[Security|Security]]) |

The limits are enforced on the bytes actually read, not on sizes the archive declares. Ignored files count toward both limits, and a zip with too many entries is refused before its list of entries is read into memory. Unpacking stops when the upload is cancelled.

## Publishing

| Way | How |
|---|---|
| Web interface | open the site and upload an archive |
| GitHub Actions | the action `danielgtmn/ownpanel/actions/publish`; see [[GitHub Action|GitHub-Action]] |
| API | `POST /api/sites/{name}/deployments` with the raw archive as the request body |
| One-time upload link | `POST /api/uploads/{id}`, link created by an MCP client; see [[MCP|MCP]] |
| MCP | `publish_files` (small sites, files given directly, up to 2000 files and 10 MB) or `create_upload_link` (archives) |

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  --data-binary @site.tar.gz \
  https://panel.example.com/api/sites/demo/deployments
```

Publishing needs write access to the site. The files are unpacked into a new, immutable version and made live in one step, unless you publish without making it live.

### Publishing without making it live

A new version can also be added while the live one stays: visitors keep seeing the current version, and the new one waits in the version list (marked "Not live yet") until someone makes it live. If previews have addresses, it can be checked there first.

| Way | How |
|---|---|
| Web interface | turn off **Make it live right away** before uploading |
| API | `POST /api/sites/{name}/deployments?live=false` |
| GitHub Actions | input `live: "false"`; the output `preview-url` is the version's preview address |
| Upload link | MCP `create_upload_link` with `live: false` |
| MCP | `publish_files` with `live: false` |

A site whose first version is published this way keeps answering "no content yet" on its domains until a version is made live. Who may publish this way is the same as for publishing: write access to the site.

## Building a site from its source

On a server where builds are set up ([[Installation|Installation#building-sites]]), the panel can also build a site and publish what the build produces: the source goes to the isolated build service, the build command runs there (for example `npm ci && npm run build`), and the output folder (for example `dist`) becomes a new version through the same path as an uploaded archive, with the same limits and checks. A site linked to a repository is built on every push and with **Build now**, with its [build settings](#build-settings) ([below](#building-from-the-linked-repository)); an administrator can also build from an uploaded source archive:

| Way | How |
|---|---|
| API | `POST /api/sites/{name}/builds` with the parts `settings` and `source`; see [[API|API]] |
| MCP | `build_site` with the source files; see [[MCP|MCP]] |

```sh
cat > settings.json <<'JSON'
{"command": "npm ci\nnpm run build", "output": "dist", "image": "node-24",
 "env": {"NODE_ENV": "production"}, "secrets": {}, "live": false}
JSON
tar -czf source.tar.gz --exclude node_modules --exclude .git .
curl --fail-with-body -H "Authorization: Bearer $TOKEN" \
  -F 'settings=<settings.json' -F 'source=@source.tar.gz' \
  https://panel.example.com/api/sites/demo/builds
```

- `command` runs with `sh` in the source folder and stops at the first failing line. `output` is relative to the source; `.` is the source itself. `image` is one of the build images (`node-24`, `node-22`; `GET /api/builds/status` lists them).
- `env` and `secrets` are environment variables of the build. The values of `secrets` are replaced with `***` in the build's log; neither appears in the panel's log, the activity log, error messages or answers. Masking is a courtesy, not a barrier ([[Security|Security#builds]]): give a build only the secrets it needs.
- The answer comes when the build has ended. A build that **fails, is cancelled or runs out of time never changes the site**: no version is added and the live version stays. An output that contains a link, a device or anything but plain files and folders, or that is too large, fails the build the same way. After success the output is a new version, live or not (`live`), exactly as if it had been uploaded.
- **One build per site at a time.** A build requested while another of the same site runs waits; a newer request replaces one that waits (it ends as `skipped`). The build service also limits how many builds run on the server at once; when too many wait there, the request is refused with `429` and can be repeated later.
- The panel waits for a build at most the longest build time plus half an hour (90 minutes); closing the request cancels the build. Builds that run longer than about an hour may lose their answer: the HTTP request is limited too.
- When the panel restarts, builds the build service still runs for it are stopped and removed: their requests are gone, so nobody would publish them.

### Building from the linked repository

A site [linked to a repository](#source-repository-and-branch) with **publish on push** on is built on every push to its branch, and whenever someone presses **Build now** in the **Builds** section of the site page. Each is a **run** ([[GitHub|GitHub#what-happens-on-a-push]]):

1. The panel downloads the commit from GitHub with a token that may only read that one repository (at most about 1 GB, within 15 minutes).
2. It builds it with the site's [build settings](#build-settings). A site without a command is published as it is, still through the build environment, so its files get the same checks.
3. On success the output becomes a new version and is made live. A failed, cancelled or timed-out build changes nothing.

- **One build per site at a time.** A push while a build runs waits; a newer one replaces it before it starts.
- **Output:** **Details** of a run shows what the build printed, live while it builds. The panel keeps it with the run (the last 30 runs per site), up to 1 MB; a longer output keeps its beginning and its last 64 KB. Secret values are hidden as `***`.
- **Cancel** in the Builds section (or in the details) stops a run that waits or builds; the site stays as it is.
- **Who may:** building, cancelling and changing the build settings need the same rights as changing the site (its owner or an administrator, a `write` token, a token limited to sites only for its sites). Seeing runs, their output and the build settings (never secret values) needs the right to see the site. The build runs isolated from the panel and from other sites ([[Security|Security#builds]]), which is what lets owners who are not administrators choose its commands.
- Without the build environment on the server, runs wait and the site page says so.

## Versions

Every publish creates a version. Its id starts with a UTC timestamp, for example `20260131T120000.000Z-ab12cd`, so ids sort by time.

The panel keeps the newest five versions of a site. Besides them it always keeps two more, however many versions were published after them without being made live:

- the **live version**, and
- the version **going back** would return to: the newest version older than the live one that was live itself.

Older versions are removed when a new one is published. So a burst of previews (for example one per pull request) can push out older previews, but never what is live or the version you would roll back to.

Making a version live is an atomic switch of a symlink (`current`), so visitors never see a half-written site, and no web server reload is needed.

### Making a version live

- Web interface: pick a version in the site's version list.
- API: `POST /api/sites/{name}/rollback` with `{"release": "<id>"}`, or with no body to go back to the version before the live one.
- MCP: `make_version_live` with an optional `version`.

Going back with no id returns to the last version before the live one that was live itself: versions that were published without being made live, and never were live, are skipped. It fails with a conflict if there is no such version. Making an older version live does not delete newer ones; they remain until pruned.

## Previews

Every kept version, live or not, can be opened on an address of its own once an administrator has set a **domain for previews** (Settings → Previews; see [[Configuration|Configuration]]). Without it, versions can still be published without making them live, but they have no address.

- **Address:** `https://<label>.<preview domain>/`. The label is 26 lowercase letters and digits derived with a secret of this panel from the site name and the version id (130 bits); it reveals neither and cannot be guessed. The site's versions list, the API (`versions[].preview_url`), MCP and the GitHub Action give the full link.
- **The link carries a key** (`?ownpanel-preview=…`, 160 bits). Opening the link stores the key in a cookie for that one address (7 days) and removes it from the address bar; without the key the preview answers `403`. The key is needed because the address alone is not secret: with public certificates, every opened preview's name is published in the certificate logs.
- **Anyone with the link** can open the version. Share it only with people who should see it.
- Answers carry `X-Robots-Tag: noindex, nofollow`.
- A preview disappears when its version is removed (pruned or the site deleted); its address then answers `404` "Unknown preview.". Addresses stay the same across restarts and when the preview domain changes only the domain part changes; a backup restored without its `secret.key` gives every version a new address.
- **Certificates** are obtained only when a preview is opened for the first time, one per opened version, and only after the web server asked the panel that this exact name is a kept version. Names below the preview domain that are not previews get no certificate. See [[Security|Security]].
- The preview domain and everything below it are reserved: no site, redirect, panel domain or mail server name may be below it.

## Source: repository and branch

A site can be linked to a **repository and branch** its code comes from, with **publish on push** on or off. On the site page, the **Source** section shows the repository (with a link to it), the branch, whether a push publishes, and who linked it; **Link repository** or **Change** opens a dialog to choose the repository and one of its branches, **Unlink** removes the link.

A push to the linked branch is recorded as a run of the site and built with the site's [build settings](#build-settings) ([above](#building-from-the-linked-repository)). On a server without the build environment, runs wait: the answers of the API and MCP say so with `"publishing_available": false`, and you publish files as before.

- **Where repositories come from:** a GitHub account or organisation connected under Settings → GitHub ([[GitHub|GitHub]]), or **any Git repository by its address** with a deploy key the panel makes for the site ("Other Git host" in the dialog, [[Git repositories|Git-Repositories]]). The panel keeps GitHub's numeric id of the repository, so a renamed repository stays linked; the name shown is refreshed whenever the panel sees the repository.
- **Who may link:** linking (and changing the branch or publish on push, which links anew) needs the same rights as changing the site (its owner or an administrator, a `write` token, a token limited to sites only for its sites) **and** the repository must be usable by that person: only administrators may use the connected repositories. A member therefore cannot link their site to an organisation's repository; to them those repositories do not exist. Members see a link an administrator made on their site and may unlink it.
- **Checks:** the panel asks GitHub, as the connection, whether the repository is still shared with the panel and whether the branch exists, and refuses otherwise. Branch names follow Git's rules: at most 255 characters, no spaces or control characters, none of `~ ^ : ? * [ \`, no `..`, `//` or `@{`, no leading `-` or `/`, no part starting with `.` or ending in `.lock`, not `@` or `HEAD`.
- **Status:** `linked`; `connection_missing` (the GitHub connection or its installation was removed: link again or unlink); `suspended` (access is paused on GitHub); `repository_missing` (the connection no longer sees the repository); `unchecked` (GitHub could not be asked; `problem` says why).
- A site has at most one source. Deleting the site deletes its link; a new site of the same name starts without one. Linking and unlinking appear in the activity log (repository, branch and publish on push; never a token).
- **Repositories by address** follow their own rules: anyone who may change the site may link it (members too), the person confirms the server's key, adds the deploy key and the notification address at the Git host, and the status is `linked`, `not_checked`, `access_failed` or `host_key_changed`. See [[Git repositories|Git-Repositories]].

## Recognised kinds of sites

**Build settings** on the site page (and `GET /api/sites/{name}/build-settings/proposal`, MCP `propose_site_build_settings`) looks at the newest commit of the linked branch, recognises how the site is made and proposes a build command, the folder with the finished site and the build environment, with notes on what to check. A site without saved settings gets the proposal filled in when the dialog opens. The proposal is a starting point you confirm or change; nothing is saved until you do.

| Kind | Recognised by | Proposed folder | Built on this server |
|---|---|---|---|
| Next.js | `next` in `package.json`, `next.config.*` | `out` (or `distDir`) | only with `output: 'export'` in the Next.js configuration; without it the proposal says so |
| Astro | `astro` in `package.json`, `astro.config.*` | `dist` (or `outDir`) | yes |
| Eleventy | `@11ty/eleventy` in `package.json`, `.eleventy.js`, `eleventy.config.*` | `_site` (or the `output` folder) | yes |
| Vite | `vite` in `package.json`, `vite.config.*` | `dist` (or `build.outDir`) | yes |
| Hugo | `hugo.toml`/`.yaml`/`.json`, or a `config.toml` with Hugo settings | `public` (or `publishDir`) | not yet: build elsewhere and publish the result |
| Jekyll | `_config.yml` and a `Gemfile` that uses Jekyll | `_site` (or `destination`) | not yet: build elsewhere and publish the result |
| Plain HTML | `index.html` and no build script | the files as they are | nothing to build |

- **Package manager:** taken from the lock file (`package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`) or the `packageManager` field of `package.json`. npm, pnpm and Yarn are supported; a site that uses Bun is built with npm, with a note. Without a lock file, packages are installed fresh on every build, and the proposal says so.
- **Node.js version:** taken from `.nvmrc`, `.node-version` or `engines.node` in `package.json`. If the server does not offer that version, the closest newer one (or else the newest) is proposed, with a note.
- **When several kinds are found** (an Astro site with an Eleventy configuration, say), the proposal follows the more specific one and names the others so you can check.
- **What is read:** the panel downloads the commit once (at most 256 MB, within 2 minutes) and keeps only the plain files at the top of it (up to 1 MB each) in a temporary folder, which it removes afterwards. Of those, only a few small ones are read, each up to a size limit. Configuration files are read as text, never run, so a setting that is computed in the configuration is not seen; links are ignored.

### Build settings

Each site has its own build settings, kept apart from its link to a repository (unlinking keeps them). Change them in **Build settings** in the Builds section, with `PUT /api/sites/{name}/build-settings` or MCP `set_site_build_settings`. Until they are saved, runs of the site fail and ask for them.

- **Variables** are stored encrypted as a whole. Values of **secret** variables are never shown again, in the web interface, the API or MCP: only their names. To keep a stored value, save the variable without a value; to change it, enter a new one. Changes appear in the activity log with the variable names only.

| Setting | Rule |
|---|---|
| Build command | Up to 16 KB of text; lines run one after another and the build stops at the first failing one. No control characters except tabs and line breaks. Empty means the folder is published as it is. |
| Folder with the finished site | A folder inside the source (`dist`, `public/site`, `.`): no absolute path, no `..`, at most 255 characters. |
| Build environment | One of the environments this server offers (Node.js 22 and 24 today). |
| Environment variables | Up to 100; names of capital letters, digits and `_`, not starting with a digit; values up to 32 KB, 256 KB together. Secret values must be at least 4 characters long and are hidden as `***` in the build log. |

Some variable names are refused because the build environment sets them itself or because they would change what runs before your command does: `PATH`, `HOME`, `PWD`, `OLDPWD`, `TMPDIR`, `IFS`, `ENV`, `BASH_ENV`, `SHELLOPTS`, `PS4`, `CDPATH`, `GCONV_PATH`, everything starting with `LD_` and everything starting with `OWNPANEL_`. To use a different search path for programs, set it in the build command itself.

## Deleting

`DELETE /api/sites/{name}` (MCP: `delete_site`, which also needs `confirm_name` equal to the name) stops serving the site, then removes all its files and versions. This cannot be undone. If the web server cannot be updated at that moment, the files are removed by the next successful update; a new site of the same name always starts empty. Access tokens limited to certain sites cannot delete sites, and the site name is removed from the lists of tokens that named it.

A user who still owns sites cannot be deleted; delete the sites first (there is no way to change a site's owner; see [[Users and Access|Users-and-Access]]).

## When the web server is behind

If the database change is saved but Caddy could not be updated (for example, Caddy is down), the API answers `502` with "saved, but the web server could not be updated yet". The background loop keeps retrying every few seconds and then every 30 seconds ([[Architecture|Architecture]]).
