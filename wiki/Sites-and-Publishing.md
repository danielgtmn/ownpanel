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

On a server where builds are set up ([[Installation|Installation#building-sites]]), the panel can also build a site and publish what the build produces: the source goes to the isolated build service, the build command runs there (for example `npm ci && npm run build`), and the output folder (for example `dist`) becomes a new version through the same path as an uploaded archive, with the same limits and checks. Building from a linked repository on every push comes with [#58](https://github.com/danielgtmn/ownpanel/issues/58); for now an administrator can build from an uploaded source archive:

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

## Deleting

`DELETE /api/sites/{name}` (MCP: `delete_site`, which also needs `confirm_name` equal to the name) stops serving the site, then removes all its files and versions. This cannot be undone. If the web server cannot be updated at that moment, the files are removed by the next successful update; a new site of the same name always starts empty. Access tokens limited to certain sites cannot delete sites, and the site name is removed from the lists of tokens that named it.

A user who still owns sites cannot be deleted; delete the sites first (there is no way to change a site's owner; see [[Users and Access|Users-and-Access]]).

## When the web server is behind

If the database change is saved but Caddy could not be updated (for example, Caddy is down), the API answers `502` with "saved, but the web server could not be updated yet". The background loop keeps retrying every few seconds and then every 30 seconds ([[Architecture|Architecture]]).
