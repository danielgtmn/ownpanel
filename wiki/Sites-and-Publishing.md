# Sites and Publishing

A **site** is a set of static files (HTML, CSS, JavaScript, images) served on one or more domains with automatic HTTPS.

## Sites

| | Rule |
|---|---|
| Name | Lowercase letters, digits and hyphens, 1 to 63 characters, starting and ending with a letter or digit. The name identifies the site in the API, in MCP and on disk; it is not a domain and cannot be changed. |
| Owner | The user who created the site. Members see and manage only their own sites; admins see all. |
| Domains | A list, possibly empty. Stored in lowercase, duplicates removed. Each must be a plain host name with at least one dot (no wildcards, no ports, no paths). |

A domain can belong to only one site. The panel's own domain (`-panel-domain`) cannot be used by a site. If a domain is taken by a site you cannot see, the error says only that it is used by another site.

Creating a site and changing its domains is one operation: `PUT /api/sites/{name}` with `{"domains": [...]}` creates the site if the name is free (you become the owner) and replaces the domain list if you may already see it. In MCP these are `create_site` and `set_site_domains`.

Changing domains replaces the whole list: domains not listed stop being served.

### What visitors see

| Situation | Answer |
|---|---|
| Site has a live version | the files, compressed with zstd or gzip |
| Site has no version yet | `503` with "This site has no content yet." |
| Site has no domains | not served |
| Host that no site claims | `404` with "Unknown site." |

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

The limits are enforced on the bytes actually read, not on sizes the archive declares.

## Publishing

| Way | How |
|---|---|
| Web interface | open the site and upload an archive |
| API | `POST /api/sites/{name}/deployments` with the raw archive as the request body |
| One-time upload link | `POST /api/uploads/{id}`, link created by an MCP client; see [[MCP|MCP]] |
| MCP | `publish_files` (small sites, files given directly, up to 2000 files and 10 MB) or `create_upload_link` (archives) |

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" \
  --data-binary @site.tar.gz \
  https://panel.example.com/api/sites/demo/deployments
```

Publishing needs write access to the site. The files are unpacked into a new, immutable version and made live in one step.

## Versions

Every publish creates a version. Its id starts with a UTC timestamp, for example `20260131T120000.000Z-ab12cd`, so ids sort by time. The panel keeps the newest five versions of a site; older ones are removed when a new one is published. The live version is never removed.

Making a version live is an atomic switch of a symlink (`current`), so visitors never see a half-written site, and no web server reload is needed.

### Making a version live

- Web interface: pick a version in the site's version list.
- API: `POST /api/sites/{name}/rollback` with `{"release": "<id>"}`, or with no body to go back to the version before the live one.
- MCP: `make_version_live` with an optional `version`.

Going back with no id fails with a conflict if there is no earlier version. Making an older version live does not delete newer ones; they remain until pruned.

## Deleting

`DELETE /api/sites/{name}` (MCP: `delete_site`, which also needs `confirm_name` equal to the name) stops serving the site, then removes all its files and versions. This cannot be undone. Access tokens limited to certain sites cannot delete sites, and the site name is removed from the lists of tokens that named it.

A user who still owns sites cannot be deleted; delete the sites first (there is no way to change a site's owner; see [[Users and Access|Users-and-Access]]).

## When the web server is behind

If the database change is saved but Caddy could not be updated (for example, Caddy is down), the API answers `502` with "saved, but the web server could not be updated yet". The background loop keeps retrying every few seconds and then every 30 seconds ([[Architecture|Architecture]]).
