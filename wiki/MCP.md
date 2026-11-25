# MCP

ownpaneld speaks the Model Context Protocol, so AI assistants can manage sites: create them, publish files, go back to an earlier version. The tools call the same operations as the API and are subject to the same permission checks.

## Endpoint and authentication

- **Endpoint:** `/mcp` on the panel's address, using streamable HTTP in stateless mode (every request carries its own credentials; there is no MCP session to remember).
- **Authentication:** `Authorization: Bearer op_…` with an access token. **Only access tokens work.** The browser session cookie is never accepted, so a signed-in browser cannot use MCP. A missing, invalid or expired token gets `401` with a `WWW-Authenticate` header.
- **Rights:** the token's rights apply. A `read` token can only look; a token limited to some sites only sees and publishes those; and it never has more rights than its user has. See [[Users and Access|Users-and-Access]].
- **Reachability:** with `-panel-domain panel.example.com`, use `https://panel.example.com/mcp`. Without it, `/mcp` is only reachable on the daemon's own address (`127.0.0.1:8600`; the Host must be local), for example through an SSH tunnel.
- Browsers' cross-site requests are refused; clients that are not browsers send no `Origin` header and are unaffected.
- Requests are limited to 64 MB.
- Activity log entries show the token that was used.

Create a token under your account in the web interface (password sign-in is needed to create tokens).

## Connect Claude Code

```sh
claude mcp add --transport http ownpanel https://<panel-domain>/mcp \
  --header "Authorization: Bearer op_…"
```

Other clients need the same two things: the address and the header `Authorization: Bearer op_…`.

## Tools

Version ids come from `get_site`; a version id starts with a UTC timestamp. Errors are returned as tool errors with a readable sentence.

| Tool | Arguments | What it does |
|---|---|---|
| `list_sites` | none | Lists the sites you can manage, sorted by name. For each: name, domains, owner, id of the live version, all kept versions (newest first, each with `id`, `created_at`, `live`). |
| `get_site` | `site` | Shows one site in the same form. |
| `create_site` | `name`, `domains` (optional list) | Creates a new, empty site you own. Fails if the name or a domain is taken. Nothing is served until something is published. Needs a `write` token that is not limited to sites. |
| `set_site_domains` | `name`, `domains` | Replaces the whole domain list of an existing site. Domains not listed stop being served. Safe to repeat. |
| `delete_site` | `name`, `confirm_name` | Permanently deletes the site with all its files and versions. `confirm_name` must equal `name`, otherwise nothing happens. Needs a `write` token that is not limited to sites. |
| `publish_files` | `site`, `files` | Publishes files given directly as a new live version, replacing the previous version completely. The site must exist. See below. |
| `create_upload_link` | `site` | Creates a one-time upload link for a `.zip` or `.tar.gz` of up to 512 MB. Returns `url`, `curl_command`, `expires_at`, `max_size_mb`. |
| `make_version_live` | `site`, `version` (optional) | Switches to a kept version; without `version`, back to the version before the live one. Safe to repeat. |
| `check_site_domains` | `site` | For each domain: `domain`, `state` (`points_here`, `points_elsewhere`, `no_record`, `lookup_failed`) and `found` (addresses it points to). Needs `-server-ip` to be configured; see [[DNS|DNS]]. |
| `list_redirects` | `site` (optional) | Lists the redirects you can see, or only the path redirects of one site. Each with `id`, `owner`, `source_domain`, `source_path`, `target`, `code`, `keep_path`, `keep_query`, `created_at`. Read-only. See [[Redirects|Redirects]]. |
| `set_redirect` | `id` (optional), `source_domain`, `source_path` (optional), `target`, `code` (optional, default 301), `keep_path`, `keep_query` (optional) | Creates a redirect, or replaces the one with `id`. Without `source_path` the whole domain redirects; with it, one path (`/old`) or a prefix (`/blog/*`) on a domain of one of your sites. Returns the redirect. |
| `delete_redirect` | `id`, `confirm_source` | Deletes a redirect. `confirm_source` must equal its `source_domain` followed by its `source_path` (like `www.example.com` or `example.com/old`), otherwise nothing happens. |

Tools that change something need a `write` token. Read-only tools are `list_sites`, `get_site` and `check_site_domains`.

### `publish_files`

`files` is a list of `{path, content, encoding}`:

| Field | Meaning |
|---|---|
| `path` | relative to the site root, forward slashes, like `index.html` or `css/site.css`; no leading slash, no `..`, no empty or `.` segments |
| `content` | the file content |
| `encoding` | `utf8` (default) for text, `base64` for binary files such as images (standard alphabet, with padding) |

Limits: at least one file, at most 2000 files, 10 MB in total after decoding. A path cannot be given twice, and a path cannot be both a file and a folder. Include every file of the site, since files of earlier versions are not carried over. For bigger sites use `create_upload_link`.

### Upload links

A link looks like `https://<panel-domain>/api/uploads/<id>` (on the panel domain if `-panel-domain` is set, otherwise on the address the request came in on). The `curl_command` is:

```sh
curl --fail-with-body -X POST --data-binary @site.zip '<url>'
```

- Works **once** (the first attempt uses it up, successful or not), for **15 minutes**, only for the one site, and publishes as the person who created it. Treat the URL like a password.
- The link itself is the credential: no other authentication is needed for `POST /api/uploads/{id}`.
- Unknown, used and expired links all answer `404`.
- A person can have at most 20 unused links.
- Links live in memory, so a restart of ownpaneld invalidates them. A deleted token or changed role also affects its open links: they are re-checked when used.

## Not available

DNS connections and records cannot be managed through MCP; use the web interface or the [[API|API]].
