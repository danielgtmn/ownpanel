# API

The panel's web interface uses the same HTTP API that scripts can use. It is served on the panel's address under `/api/`. Requests and responses are JSON, except archive uploads, which send the raw file as the request body.

## Authentication

| Client | How |
|---|---|
| Programs (scripts, CI) | `Authorization: Bearer op_…` with an access token |
| Browsers | the session cookie `__Host-ownpanel_session` (over plain HTTP: `ownpanel_session`), set by `POST /api/login` or `POST /api/setup` |

- Access tokens are described in [[Users and Access|Users-and-Access]].
- If an `Authorization` header is present it must be `Bearer <token>`; a wrong token is refused right away with `401`. Otherwise the cookie is used. A stale cookie just means nobody is signed in.
- **Cookie requests that change something** (anything but `GET` and `HEAD`) must send the header `X-Ownpanel-Request: 1`, otherwise they get `403`. Token requests do not need it.
- If an `Origin` header is sent on a changing request without a token, its host must equal the request's `Host`, otherwise `403` ("requests from other web sites are not allowed"). The API never allows cross-origin requests (no CORS).
- Tokens and passwords can only be managed when signed in with a password, not with a token.
- Some routes are open: `GET /api/session`, `POST /api/setup` (needs the setup code), `POST /api/login`, `POST /api/logout`, `POST /api/password-reset`, `POST /api/password-reset/confirm` (the link is the credential), and `POST /api/uploads/{id}` (the link is the credential).

```sh
curl -H "Authorization: Bearer op_…" https://panel.example.com/api/sites
```

## Errors

Errors are JSON with a readable sentence:

```json
{"error": "Site not found"}
```

| Status | Meaning |
|---|---|
| `400` | bad input: invalid JSON, name, domain, archive, password, setup code, record |
| `401` | not signed in, wrong credentials, or an invalid token |
| `403` | known, but not allowed (read-only or site-limited token, not an admin, missing header) |
| `404` | missing, or not visible to you (the two look the same); unknown endpoint |
| `422` | a build ran and failed; the answer carries its result |
| `409` | cannot be done in the current state: name taken, nothing to go back to, last admin, user still owns sites, setup already done, another import of the mailbox running |
| `413` | a file or part of an upload is too large |
| `429` | the build service is busy, too many failed sign-in attempts, too many sign-ins at the same moment, Hetzner's or GitHub's request limit was reached, or too many mail imports in an hour |
| `502` | the change is saved but the web server could not be updated yet (the loop retries), or an outside service such as Hetzner, GitHub or another mail server is unavailable or refused (for an import: its answer, never the password) |
| `507` | not enough free disk space on the server (mail import) |
| `500` | something went wrong on the server; the details are only in the log |

Responses carry `Cache-Control: no-store`. Request bodies that are JSON are limited to 1 MB; archives to 512 MB.

## Ids

Every id the API returns or takes is a **string**: `"id": "42"`, `"connection_id": "7"`, `"mailbox_id": "12"`, and the same text in paths (`/api/redirects/42`). Treat ids as opaque: do not parse them as numbers, compare them or rely on their order; ids created later may not be digits. Records that existed before keep their number as text. A path id that does not name anything you may see answers `404`. This includes the id of a mail import (`"id": "42"`, newer ones like `"01jx3k5m7n9p1q3r5s7t9v1w3x"`).

## Objects

**Site:** `{"name", "domains": [..], "owner", "current_release", "releases": [..], "versions": [Version, …], "preview_addresses": bool}`. `releases` lists version ids oldest first; `current_release` is empty until a version is live. `versions` lists the same versions newest first with their state; `preview_addresses` tells whether versions can be opened on an address of their own (an admin set a domain for previews).

**Version:** `{"id", "live": bool, "pending": bool, "preview_url"}`. `pending` is true for a version published without making it live that has not been live since. `preview_url` opens the version (live or not) and works for anyone who has it; `""` without a domain for previews.

**Publication** (the answer to publishing): a Site plus `{"published_release", "published_live": bool, "preview_url"}` for the new version. When only the web server could not be updated (`502`), the answer carries the Publication next to `error`: the version exists.

**Build result** (the answer to building a site): `{"status", "reason", "message", "exit_code", "log_tail", "publication"}`. `status` is `succeeded`, `failed`, `cancelled` or `skipped` (a newer build of the site replaced it before it started); `reason` says why it failed: `exit` (the command failed, `exit_code` is set), `timeout`, `memory`, `disk`, `output` (the output folder is missing, holds something that is not a plain file or folder, is too large or cannot be published), `source`, `unprotected` or `internal`. `log_tail` is the end of the build's log with the values of `secrets` replaced by `***`; it is left out when a secret is shorter than 4 characters. `publication` is the Publication after success.

**User:** `{"id", "name", "email", "role", "created_at"}`, role `"admin"` or `"member"`; `email` is `""` when the person has none.

**Token:** `{"id", "name", "access", "sites", "created_at", "last_used_at", "expires_at", "expired"}`. `sites` is `null` for all sites; `expires_at` is `null` for a token that never expires. Creating a token also returns `"token"` (the secret, shown once).

**Activity:** `{"id", "at", "actor", "action", "target", "detail"}`.

**DNS connection:** `{"id", "provider", "name", "owner", "created_at", "last_checked_at", "last_error", "can_write", "zone_count"}`. `can_write` and `zone_count` are `null` while unknown; the token is never returned.

**Record set:** `{"name", "type", "ttl", "values": [..], "locked"}`. `name` is `@` for the zone itself; `ttl` is `null` for the zone default; TXT values are plain text.

**GitHub connection:** `{"id", "app_id", "name", "slug", "owner", "owner_type", "html_url", "settings_url", "created_by", "created_at", "last_checked_at", "last_error", "installations": [{"id", "account", "account_type", "repository_selection", "suspended", "repository_count", "settings_url", "sites"}, …]}`. `sites` lists the sites linked to one of the installation's repositories, `[{"site", "repository", "repository_id", "branch"}, …]` (only in `GET /api/github` and `POST /api/github/refresh`, otherwise empty). `owner_type` and `account_type` are `"user"` or `"organization"`; `repository_selection` is `"all"` or `"selected"`; `repository_count` is `null` until listed. `id` is the panel's id of the connection, a string like every id the panel makes; `app_id`, the installations' `id` and the repositories' `id` are GitHub's own ids and stay numbers. The app's key and secrets are never returned.

**GitHub repository:** `{"id", "installation_id", "owner", "name", "full_name", "private", "archived", "default_branch", "html_url"}`.

**Site source:** `{"site", "source", "publishing_available", "last_run"}`. `source` is `null` when the site is not linked, else `{"kind", "repository", "repository_id", "installation_id", "account", "url", "branch", "publish_on_push", "status", "problem", "linked_by", "linked_at"}`: `kind` is `"github"`; `repository` is `owner/name` as last seen; `installation_id` is left out once the connection was removed; `status` is `linked`, `connection_missing`, `suspended`, `repository_missing` or `unchecked` (then `problem` says why). `publishing_available` is `false` while pushes are not built and published yet. `last_run` is the site's newest run or `null`. See [[Sites and Publishing|Sites-and-Publishing#source-repository-and-branch]].

**Run:** `{"id", "site", "trigger", "status", "repository", "ref", "branch", "commit", "short_commit", "commit_url", "message", "author_name", "author_login", "pusher", "detail", "waiting", "created_at", "started_at", "finished_at"}`. `id` is a string; `trigger` is `"push"`; `status` is `queued`, `running`, `succeeded`, `failed`, `cancelled` or `skipped` (replaced by a newer push before it started); `commit` is the full commit id; `commit_url` its page on GitHub; `message` (first line, at most 200 characters), `author_name` and `pusher` come from the repository and are plain text, not markup; `waiting` is set on a queued run while the server cannot build yet; `detail` says why a run was skipped, failed or cancelled. See [[GitHub|GitHub#what-happens-on-a-push]].

**Redirect:** `{"id", "owner", "source_domain", "source_path", "target", "code", "keep_path", "keep_query", "created_at"}`. `source_path` is `""` for a whole-domain redirect, otherwise `/exact` or a prefix `/path/*`; `code` is `301`, `302`, `307` or `308`. See [[Redirects|Redirects]].

Domains whose DNS is managed elsewhere: kept in your panel as domains of their own, with nothing at any DNS provider. Each person has their own list; another person's entry answers `404`, also for administrators.

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/domains` | anyone, not site-limited | | list of Domain, by name |
| `POST` | `/api/domains` | password sign-in or unrestricted write | `{"name": "example.com"}` | Domain. Only registrable domains (no subdomains, no IP addresses, non-ASCII in `xn--` form); `400` otherwise. Adding it again is fine |
| `DELETE` | `/api/domains/{domain}` | same | | `{"name", "used_by"}`. Removes only the entry; nothing changes at any provider. `used_by` lists what of yours still uses a name in the domain, so it stays in the panel through that. `404` if it is not in your list |

**Domain:** `{"name", "created_at", "used_by"}`; `used_by` as for Zone below.

**Zone:** `{"connection_id", "connection_name", "name", "read_only", "nameservers": [..]}`. `nameservers` are the provider's nameservers to enter at the domain's registrar.

**Mail domain:** `{"domain", "owner", "active", "activated_at", "checked_at", "problem", "mailboxes", "aliases", "dkim_selector", "dkim_record", "created_at", "organisation"}`. `organisation` is `null` when the domain stands alone, else `{"id", "name", "own", "domains": [..]}`: `own` is false when an administrator put the domain into someone else's organisation, `domains` are its other domains you may see. `active` turns true once the domain's MX record (asked at its authoritative nameservers) points to the mail host; only then is mail received for it. `problem` says why it is not active yet. `dkim_record` is the public TXT value for `<dkim_selector>._domainkey.<domain>`; the private key is never returned.

**Mailbox:** `{"id", "address", "domain", "local_part", "owner", "quota_bytes", "send_per_hour", "send_per_day", "created_at", "display_name", "app_passwords_only", "usage", "kind", "keep_sent_copy", "shared_with", "access_to"}`. `quota_bytes` `0` means no limit. `display_name` (may be empty) is shown as the sender's name; with `app_passwords_only` mail programs sign in only with app passwords. `usage` is `{"used_bytes", "messages", "blocked", "blocked_reason", "blocked_at"}` as reported by the mail service, or `null` when it could not be asked. `kind` is `personal` or `shared` (nobody signs in; people with access use it with their own mailbox); `keep_sent_copy` (shared only) keeps mail sent for it in its Sent folder. `shared_with` lists the mailboxes with access to this one, `[{"mailbox_id", "address", "display_name", "rights", "active"}]`; `access_to` those this one may use, `[{"mailbox_id", "address", "kind", "rights", "active"}]`. Rights are `read`, `write` (includes read), `manage`, `send-as` and `send-on-behalf`; `active` is false when the access is not in effect (the two domains are no longer in the same organisation). Password hashes are never returned.

**Organisation:** `{"id", "name", "owner", "domains": [..], "created_at"}`. `domains` are only those you may see (an administrator may have put someone else's domain into your organisation). Groups mail domains; mailboxes share only within one organisation (see [[E-Mail|E-Mail]]).

**App password:** `{"id", "name", "created_at", "last_used_at", "last_used_network"}`. `last_used_network` is the /24 (IPv4) or /48 (IPv6) of the last sign-in, never the full address. Only the answer to creating one also has `"password"`; it is never shown again.

**Alias:** `{"id", "address", "domain", "local_part", "catch_all", "targets": [..], "owner", "created_at", "sender_policy", "hide_from_directory"}`. Local part `*` is the domain's catch-all. `sender_policy` says who may write to it (a distribution group): `anyone`, `organisation` (addresses of the domain's organisation) or `members` (the addresses it delivers to); `hide_from_directory` leaves it out of the organisation's address book (once that exists).

**Mail status:** `{"mail_host", "service_set_up", "reachable", "in_sync", "problem", "listeners": [{"name", "address", "listening", "error"}], "certificate_expires", "queue_length", "client": {"server", "imap_port", "smtp_port", "smtp_starttls_port", "user_name_is_address"}}`.

**Mail records:** `{"domain", "mail_host", "records": [{"kind", "type", "name", "fqdn", "want", "found", "status", "note"}], "can_apply", "connection_id", "zone", "apply_note"}`. `kind` is `mx`, `spf`, `dkim`, `dmarc`, `srv-submissions`, `srv-imaps`, `autoconfig`, `autodiscover`, and, when the mail server name serves calendars and contacts, `srv-caldav`, `txt-caldav`, `srv-carddav` and `txt-carddav` (SRV `_caldavs._tcp`/`_carddavs._tcp` `0 1 443 <mail host>.` and TXT `path=/dav/` at the same names, `"optional": true`, written with the others); `autoconfig` and `autodiscover` are CNAMEs to the mail host with `"optional": true` (recommended, mail works without them), judged by where the name leads: `ok` when it resolves to the server's addresses only (a CNAME to the mail host or A/AAAA records), `missing`, `different` (it leads elsewhere; `found` lists those addresses) or `unknown` (also when the server's addresses are not set). Applying creates the CNAME where the name is unused, replaces a CNAME that leads elsewhere and skips names with other records; `found` are the values the domain's authoritative nameservers answer; `status` is `ok`, `missing`, `different`, `conflict` (needs a person, never changed automatically: several SPF or DMARC records, SPF with `+all`/`?all` or more than 10 lookups) or `unknown` (the lookup failed). For SPF, `want` is the existing record with `mx` added; an existing DMARC record is `ok` whatever it says. `can_apply` is true when the domain lies in a zone of exactly one of **your own** DNS connections with write access (never someone else's, also not for admins) and you may write; `connection_id` and `zone` then name it and `name` is relative to that zone, otherwise `connection_id` and `zone` are empty, `name` is relative to the mail domain and `apply_note` says why.

**Mail record changes:** `{"changes": [{"kind", "type", "name", "action", "old", "new", "needs_confirm", "note", "optional"}], "applied"}`. `action` is `create`, `replace`, `add` (to other TXT values, which stay), `keep` or `skip`. Only replacing MX records that do not point to the mail host has `needs_confirm` (mail would move here). `applied` is `false` only when such a change was not confirmed; nothing was written then.

**Mail health:** `{"mail_host", "checks": [{"id", "state", "found", "expected", "detail", "can_apply"}]}` with the checks `service`, `mail_host_dns`, `mail_host_spf`, `ptr`, `outbound_port`, `certificate`, `listeners`, `postmaster`, `blocklists`, `self_service` in this order; `state` is `ok`, `warning`, `error` or `unknown` (a lookup or the mail service could not be asked). IPv4 addresses without a PTR naming `mail_host` are an error, IPv6 ones a warning. The outbound port check is reused for 10 minutes. `mail_host_spf` looks at the SPF record of `mail_host` itself (bounces and SRS-forwarded mail use it as sender domain): `expected` is `v=spf1 a -all`, or the existing record with `a` added; a missing or unmerged record is a `warning`, an SPF record that cannot be merged safely a `warning` without `expected`. `can_apply` is true (only on this check) when an admin's own DNS connection holds the zone of `mail_host`. `self_service` says whether mailbox users have their own sign-in page on `mail_host`: a `warning` when `mail_host` equals the panel domain, the mail service has no web socket (`-mail-web`) or there is no certificate yet, an `error` when the mail service could not set up its web socket (`found` says why).

**Record template:** `{"id", "title", "description", "params": [{"id", "label", "description", "placeholder", "optional"}], "own_mail"}`. `own_mail` marks the entry for e-mail on this server, which has no records of its own here: set them up with `POST /api/mail/domains/{domain}/records`. **Template result:** `{"template", "zone", "changes": [{"name", "type", "action", "current", "new", "note"}], "applied"}`; `action` is `add`, `change`, `ok` (already as it should be) or `conflict` (cannot be set up automatically, `note` says why); `current` and `new` are all values of the record set before and after (for TXT including the values that are kept).

## Routes

"Anyone" means any authenticated person or token. "Write" means a token needs `write` access; limited tokens work only on their sites. "Admin" means the admin role with a password sign-in or an unrestricted `write` token. Details are in [[Users and Access|Users-and-Access]].

### Session and account

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/session` | open | | `{"setup_required": bool, "setup_complete": bool, "user": User or null, "via": "session" or "token" or null, "password_reset": bool}`; `password_reset` tells whether a forgotten password can be reset by e-mail |
| `POST` | `/api/setup` | open, needs setup code | `{"setup_code", "name", "password"}` | `201` User; sets the session cookie. `409` once set up |
| `POST` | `/api/login` | open | `{"name", "password"}`; `name` may be the e-mail address | User; sets the cookie. `401` wrong credentials, `429` throttled |
| `POST` | `/api/password-reset` | open | `{"email"}` | `204` whether or not the address belongs to someone; `400` not an address, `404` the panel cannot send e-mail, `429` too many requests |
| `POST` | `/api/password-reset/confirm` | open | `{"token", "password"}` | `204`; ends all sessions of that person. `400` link invalid, used or expired, or password not allowed |
| `POST` | `/api/logout` | open | | `204`; clears the cookie |
| `PUT` | `/api/account/password` | password sign-in | `{"current", "new"}` | `204`; ends your other sessions |
| `PUT` | `/api/account/email` | password sign-in | `{"email", "current"}`; `""` removes the address; `current` is your current password | User. `400` invalid address or missing or wrong current password, `409` the address belongs to someone else, `429` too many wrong passwords |
| `POST` | `/api/account/logout-everywhere` | password sign-in | | `204`; ends all your sessions, this one included, and clears the cookie. Tokens keep working |

### Access tokens

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/tokens` | password sign-in | | list of Token (your own) |
| `POST` | `/api/tokens` | password sign-in | `{"name", "access": "read" or "write", "sites": null or ["name", …], "expires_in_days": null or 1 to 3650}` | `201` Token with `token`. `expires_in_days` missing or `null`: never expires |
| `DELETE` | `/api/tokens/{id}` | password sign-in | | `204` |

### Sites

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/sites` | anyone | | list of Site you may see |
| `GET` | `/api/sites/{name}` | anyone, own site | | Site |
| `PUT` | `/api/sites/{name}` | write; creating needs an unrestricted token | `{"domains": ["example.com"]}` | Site. Creates the site if the name is free, otherwise replaces its domains |
| `DELETE` | `/api/sites/{name}` | write, unrestricted | | `204` |
| `POST` | `/api/sites/{name}/deployments` | write, own site | raw `.zip` or `.tar.gz` body; `?live=false` adds the version without making it live | `201` Publication |
| `POST` | `/api/sites/{name}/rollback` | write, own site | optional `{"release": "<id>"}`; no body means the last earlier version that was live | Site |
| `GET` | `/api/sites/{name}/icon` | anyone, own site | | the icon of the live version: a `<link rel="icon">` (or `shortcut icon`, `apple-touch-icon`) in `index.html` pointing at a file in the same version or a `data:` URL up to 64 KB, else `favicon.svg`, `favicon.png`, `favicon.ico` or `apple-touch-icon.png`; PNG, ICO, SVG, WebP, GIF or JPEG up to 256 KB. Nothing is fetched from elsewhere. `404` if there is none |
| `POST` | `/api/sites/{name}/builds` | admins (signed in, or an unrestricted write token) | `multipart/form-data` with two parts in this order: `settings` (JSON `{"command", "output", "image", "env": {..}, "secrets": {..}, "live"}`) and `source` (the source tree as `.tar.gz`, up to 512 MB) | `201` Build result once the build has ended. A build that ran and failed answers `422` with `"code": "build_failed"` next to the result; one replaced by a newer build of the site before it started `409` with `"code": "build_replaced"`. `429` when the build service is busy, `502` when it is unavailable. See [[Sites and Publishing\|Sites-and-Publishing#building-a-site-from-its-source]] |
| `GET` | `/api/sites/{name}/source` | anyone, own site | | Site source. Asks GitHub (through the list kept for a minute) whether the repository is still there and refreshes its name |
| `PUT` | `/api/sites/{name}/source` | write, own site, and the repository must be usable by you (administrators only) | `{"installation_id", "repository_id", "branch", "publish_on_push"}` | Site source. Links the site, replacing its link. `400` for an invalid branch name or a branch the repository does not have; `404` for a repository you may not use, one the connection does not see, or a site you may not see |
| `DELETE` | `/api/sites/{name}/source` | write, own site | | `204`; `404` if the site is not linked |
| `GET` | `/api/sites/{name}/source/repositories` | anyone, own site | | `{"connected", "repositories": [GitHub repository with "account", …], "problems": [..], "truncated"}`: the repositories you may link the site to. Empty (and `connected` false) for anyone but administrators. `problems` lists accounts that could not be listed |
| `GET` | `/api/sites/{name}/source/branches?installation_id=…&repository_id=…` | own site, repository usable by you | | `{"branches": [..], "default_branch", "truncated"}` |
| `GET` | `/api/sites/{name}/runs` | anyone, own site | | `{"site", "runs": [Run, …], "kept", "publishing_available"}`: the newest runs, newest first; `kept` is how many the panel keeps per site (30) |
| `POST` | `/api/uploads/{id}` | open (upload link) | raw archive body | `201` Publication (made live or not, as the link was created); `404` for unknown, used or expired links |
| `GET` | `/api/previews/certificate-allowed?domain=<name>` | only the web server on this machine | | `200` if the name is the preview address of a kept version, else `404`. Asked by Caddy before it obtains a certificate for a preview ([[Security|Security#previews]]) |

### Users and activity

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/users` | admin | | list of User |
| `POST` | `/api/users` | admin | `{"name", "email", "password", "role"}`; `email` optional | `201` User. `409` name or address taken |
| `PUT` | `/api/users/{id}` | admin | any of `{"role", "email", "password"}`; `""` removes the address | User; a new password ends all of that person's sessions |
| `DELETE` | `/api/users/{id}` | admin | | `204`; `409` for the last admin or a user who owns sites, domain redirects or mail domains |
| `GET` | `/api/activity?limit=50` | admin | `limit` 1 to 200 (default 50) | list of Activity, newest first |

### Server and DNS

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/server` | anyone | | `{"ips": [..]}`: the addresses given with `-server-ip` |
| `GET` | `/api/builds/status` | admins (signed in, or an unrestricted token) | | `{"available", "reason", "detail", "slots", "running", "waiting", "max_memory_bytes", "max_cpu_millis", "max_seconds", "workspace_bytes", "cache_bytes", "disk_bytes", "free_bytes", "images": [..]}`: whether this server can build sites. Without `available`, `reason` is one of `off`, `system`, `runtime`, `install`, `limits`, `disk`, `network`, `images`, `preparing`, `worker`, `not_configured`, and `detail` the technical cause ([[Installation\|Installation#building-sites]]) |
| `GET` | `/api/dns/connections` | anyone, not site-limited | | list of DNS connection (yours; admins see all) |
| `POST` | `/api/dns/connections` | password sign-in or unrestricted write | `{"provider": "hetzner", "name", "token"}` | `201` DNS connection |
| `POST` | `/api/dns/connections/{id}/check` | password sign-in or unrestricted write, your own | | DNS connection; the outcome is in `last_error` |
| `DELETE` | `/api/dns/connections/{id}` | same as above; admins may remove anyone's | | `204` |
| `GET` | `/api/dns/zones` | anyone, not site-limited | | `{"zones": [Zone, …], "problems": [{"connection_id", "connection_name", "error"}]}`: the zones of your connections that are in the panel (chosen, or used by your own site, redirect or mail domain, or for administrators the panel domain) |
| `GET` | `/api/dns/zones/available` | same | | the same shape: the zones of your connections that are not in the panel yet |
| `PUT` | `/api/dns/connections/{id}/zones/{zone}/managed` | password sign-in or unrestricted write, your own connection | | Zone. Manages the zone in the panel; nothing changes at the provider. Repeating is fine. `404` if the connection has no such zone |
| `DELETE` | `/api/dns/connections/{id}/zones/{zone}/managed` | same | | `204`. Stops managing it; nothing is deleted at the provider. `409` while something of yours uses it, `404` if it was not chosen |
| `GET` | `/api/dns/connections/{id}/zones/{zone}/records` | anyone, not site-limited, your own connection | | list of Record set |
| `PUT` | `/api/dns/connections/{id}/zones/{zone}/records/{name}/{type}` | write, unrestricted, your own connection | `{"ttl": 3600 or null, "values": [..]}` | Record set as written |
| `DELETE` | `/api/dns/connections/{id}/zones/{zone}/records/{name}/{type}` | same | | `204`; deleting a missing record set is fine |
| `GET` | `/api/dns/templates` | anyone, not site-limited | | list of Record template |
| `POST` | `/api/dns/connections/{id}/zones/{zone}/templates/{template}` | preview: anyone, not site-limited, your own connection; with `confirm`: write, unrestricted | `{"params": {"value": "…"}, "confirm": false}` (empty body is the same) | Template result. Without `confirm` nothing is written. With `confirm` every `add` and `change` is written; `409` (nothing written) if a change is a `conflict` or the zone is read-only, `400` for missing or invalid parameters and for the template `own-mail` |
| `GET` | `/api/sites/{name}/domains/status` | anyone, own site | | list of `{"domain", "state", "found": [..], "zone": null or {"connection_id", "connection_name", "zone", "record_name"}}`; `state` is `points_here`, `points_elsewhere`, `no_record` or `lookup_failed`. `409` if no `-server-ip` is set |
| `POST` | `/api/sites/{name}/domains/{domain}/point` | write, own site | | `{"changed": [Record set, …]}` |
| `GET` | `/api/dns/domain-status?domain=<domain>` | anyone; a domain of a site you may see or of a domain redirect you may see | | one element of the site domain status list above. `404` for other domains |
| `POST` | `/api/dns/point` | write; same domains | `{"domain": "www.example.com"}` | `{"changed": [Record set, …]}`, like the site route |
| `POST` | `/api/dns/connections/{id}/zones` | password sign-in or unrestricted write, your own connection | `{"name": "example.com"}` | `201` Zone, managed in the panel right away. Only registrable domains (no subdomains). `409` if it exists already |
| `DELETE` | `/api/dns/connections/{id}/zones/{zone}` | same | `{"confirm": "<zone name>"}` | `204`. `400` if `confirm` does not match; `409` while a site, redirect, mail domain or the panel domain (of anyone) uses a domain in the zone, listing them, or if the zone is protected at the provider |

**Zone:** `{"connection_id", "connection_name", "name", "read_only", "nameservers", "adopted", "used_by"}`. `adopted` is true when you chose to manage it here; `used_by` describes what of yours uses a domain in it and so keeps it in the panel; things of other people do not count here (they only block deleting the zone).

### Redirects

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/redirects` | anyone | | list of Redirect you may see (admins: all), sorted by domain and path |
| `GET` | `/api/sites/{name}/redirects` | anyone, own site | | list of the path redirects on the site's domains |
| `POST` | `/api/redirects` | write; domain redirects need a token that is not limited to sites | `{"source_domain", "source_path", "target", "code", "keep_path", "keep_query"}` | `201` Redirect |
| `PUT` | `/api/redirects/{id}` | write; owner, admin, or (path redirects) whoever may change the site | same as `POST` | Redirect |
| `DELETE` | `/api/redirects/{id}` | same | | `204` |

### GitHub

Connecting an account is a browser flow with a stop at GitHub ([[GitHub|GitHub]]). "Admin, signed in" means an administrator with a password sign-in; those steps are bound to that very sign-in. "Admin" also allows an unrestricted `write` token; "admin read" also a read-only token. Members get `403`.

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/github` | admin read | | `{"ready", "problem", "apps": [GitHub connection, …], "sites_without_connection": [{"site", "repository", "repository_id", "branch"}, …]}`. `ready` is false (and `problem` says why) without a panel domain with HTTPS; `sites_without_connection` are sites linked to a repository whose connection or installation was removed |
| `POST` | `/api/github/apps` | admin, signed in | `{"organization": "my-org"}` (`""` for your own account) | `{"url"}`: the panel's page (`/api/github/manifest?state=…`) to open in the browser; it sends you on to GitHub. `409` without a panel domain with HTTPS or when the organisation is already connected |
| `GET` | `/api/github/manifest?state=…` | admin, signed in, same sign-in | | an HTML page with the form that creates the app on GitHub, with its own content security policy |
| `GET` | `/api/github/app-created?code=…&state=…` | open (GitHub sends the browser here) | | `303` to `/#/settings?github=created&code=…&state=…`, or `github=failed` for malformed values. Changes nothing |
| `POST` | `/api/github/apps/finish` | admin, signed in, same sign-in as the start | `{"code", "state"}` | `201` GitHub connection. `400` for a used, expired, foreign or malformed `state`, or a code GitHub does not accept |
| `POST` | `/api/github/apps/{id}/install` | admin, signed in | | `{"url"}`: GitHub's page where the app is installed and the repositories are chosen |
| `GET` | `/api/github/installed?installation_id=…&state=…` | open (GitHub sends the browser here) | | `303` to `/#/settings?github=installed&installation_id=…&state=…` (`github=requested` when an organisation owner must approve first). Changes nothing |
| `POST` | `/api/github/installations` | admin, signed in, same sign-in as the start | `{"installation_id", "state"}` | GitHub connection. `400` for a bad `state`; `404` if GitHub, asked as that app, does not know the installation |
| `GET` | `/api/github/installations/{id}/repositories` | admin read | | `{"repositories": [GitHub repository, …], "truncated"}` (at most 3000; kept for a minute) |
| `POST` | `/api/github/refresh` | admin | | the same as `GET /api/github`, after asking GitHub afresh where each app is installed; problems go into `last_error` |
| `DELETE` | `/api/github/apps/{id}` | admin | | `{"name", "settings_url"}`: the stored key and secrets are gone; `settings_url` is where the app is deleted on GitHub |
| `POST` | `/api/github/webhook` | GitHub (signed with the app's secret; no session or token) | a GitHub delivery, `application/json`, at most 5 MB | `{"result", "runs"}`: `result` is `handled`, `ignored` (nothing to do) or `duplicate` (this delivery id was seen before), `runs` how many runs it created. Anything that is not a valid, signed delivery of a connected app gets the same `400` `{"error": "The delivery was not accepted"}`; `413` too large; `429` too many deliveries. See [[GitHub|GitHub#what-happens-on-a-push]] and [[Security|Security#github]] |

### E-mail

"Mail read" is anyone except tokens limited to sites; "mail write" additionally needs write access and a person (a password sign-in or a `write` token not limited to sites). Members see and change the mail domains they own; admins all. See [[E-Mail architecture|E-Mail-Architecture]].

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/mail/status` | mail read | | Mail status (includes the settings for mail programs) |
| `POST` | `/api/mail/checks/outbound` | admin (also read-only token) | | `{"ok", "target", "error"}`: whether the mail service can reach port 25 elsewhere |
| `GET` | `/api/mail/health` | admin (also read-only token) | | Mail health; checks run in parallel within 10 seconds |
| `POST` | `/api/mail/host-spf` | admin, and the zone of the mail host in one of your own DNS connections | | Mail record changes for the `mail-host-spf` record (written at once). `409` if the mail host is not set or no own connection holds its zone |
| `GET` | `/api/mail/domains` | mail read | | list of Mail domain you may see, sorted by name |
| `POST` | `/api/mail/domains` | mail write; members only for a domain of their site or domain redirect, or in a zone of their own DNS connection; admins any domain | `{"domain": "example.com"}` | `201` Mail domain. `400` if another person's site or redirect uses the domain (not naming it), if another user's sign-in address is in it or below it, or for the mail host itself, `403` for a domain you do not control, `409` if e-mail is already enabled for it or you have 20 mail domains |
| `GET` | `/api/mail/domains/{domain}` | mail read, own | | Mail domain |
| `POST` | `/api/mail/domains/{domain}/check` | mail read, own | | Mail domain, after looking at the MX record again (at most every 5 seconds) |
| `POST` | `/api/mail/domains/{domain}/activate` | admin (signed in or unrestricted write token) | | Mail domain, receiving mail at once without waiting for its MX record (for a gateway in front of the server, split DNS or tests) |
| `GET` | `/api/mail/domains/{domain}/records` | mail read, own | | Mail records; `409` until the mail host is set |
| `POST` | `/api/mail/domains/{domain}/records` | mail write, own, and the domain's zone in exactly one of your own DNS connections | `{"confirm": false}` (empty body is the same) | Mail record changes. Without `confirm`, nothing is written if a change needs confirmation; otherwise every `create`, `replace` and `add` is written through the DNS provider and the domain is checked for activation. `409` if no (or more than one) own connection holds the zone or the mail host is not set |
| `DELETE` | `/api/mail/domains/{domain}` | mail write, own | `{"confirm": "<domain>"}` | `204`; deletes all mailboxes, aliases and stored mail of the domain |
| `GET` | `/api/mail/domains/{domain}/mailboxes` | mail read, own | | list of Mailbox, in the order they were created |
| `POST` | `/api/mail/domains/{domain}/mailboxes` | mail write, own | `{"local_part", "kind", "keep_sent_copy", "password", "display_name", "quota_bytes", "send_per_hour", "send_per_day"}` (kind, display name and limits optional; `"kind": "shared"` takes no password) | `201` Mailbox. Defaults: personal, 5 GiB, 100 per hour, 500 per day; members can only choose lower values. `409` if the address is taken (also by an alias) or you have 200 mailboxes |
| `GET` | `/api/mail/mailboxes` | mail read | | list of every Mailbox you may see |
| `GET` | `/api/mail/mailboxes/{id}` | mail read, own | | Mailbox |
| `PATCH` | `/api/mail/mailboxes/{id}` | mail write, own; only admins raise limits | `{"quota_bytes", "send_per_hour", "send_per_day", "display_name", "app_passwords_only"}`, missing fields stay. Display name: at most 64 characters, no line breaks, none of `<>"`; `""` removes it | Mailbox |
| `POST` | `/api/mail/mailboxes/{id}/password` | mail write, own | `{"password"}` (10 to 200 characters; one that looks like an app password is refused) | `204`. `400` for a shared mailbox (as are app passwords and `app_passwords_only`) |
| `PUT` | `/api/mail/mailboxes/{id}/kind` | mail write, own | `{"kind": "shared"}` or `{"kind": "personal", "password"}` | Mailbox. Shared: password, app passwords and `app_passwords_only` are dropped, every session ends and the mailbox's access to other mailboxes is removed. Personal: needs a new password, turns `keep_sent_copy` off; access others have stays |
| `PUT` | `/api/mail/mailboxes/{id}/keep-sent-copy` | mail write, own | `{"keep_sent_copy": bool}` | Mailbox. `400` for a personal mailbox |
| `PUT` | `/api/mail/mailboxes/{id}/grants` | mail write, own; shared mailboxes only | `{"address", "rights": ["read", …]}` | Mailbox; gives the personal mailbox `address` access or changes its rights. `400` for a personal mailbox `{id}`, an address that is no personal mailbox of the domain's organisation, the mailbox itself, a shared mailbox, or no or unknown rights; `409` at 100 mailboxes with access |
| `DELETE` | `/api/mail/mailboxes/{id}/grants/{grantee_mailbox_id}` | mail write, own | | Mailbox; removes access to a shared mailbox, or a share the person made of their personal mailbox (those are added only by the person). Mail programs with one of its folders open are disconnected |
| `GET` | `/api/mail/mailboxes/{id}/app-passwords` | mail read, own | | list of App password, oldest first |
| `POST` | `/api/mail/mailboxes/{id}/app-passwords` | mail write, own | `{"name": "Phone"}` (the device or program, at most 64 characters) | `201` App password with `"password"`, shown this once. `409` at 50 app passwords. Also `201` when the mail server could not be updated yet (it is retried) |
| `DELETE` | `/api/mail/mailboxes/{id}/app-passwords/{app_id}` | mail write, own | | `204`; mail programs using it are signed out at once |
| `POST` | `/api/mail/mailboxes/{id}/unblock` | admin | | `204`; lifts a sending block of the mail service |
| `GET` | `/api/mail/mailboxes/{id}/rules` | mail read, own | | `{"out_of_office": {"enabled", "on", "start", "end", "set_by", "set_at", "subject", "text", "days"}, "forwarding": {"enabled", "to", "keep_copy", "set_by", "set_at"}, "hand_written", "may_forward"}`. Subject, text and the forwarding address only when they were set from the panel (`set_by` not empty); what the person set themselves shows only as on or off |
| `PUT` | `/api/mail/mailboxes/{id}/out-of-office` | mail write, own | `{"enabled", "subject", "text", "start", "end", "zone", "days", "replace"}`: dates `YYYY-MM-DD` (optional), zone `+hhmm`, days 1 to 90 (default 7); `enabled: false` clears it | the same object. `409` when the person's filters were written in a mail program and `replace` is not set. Recorded in the activity log (without the text); the person sees who set it |
| `PUT` | `/api/mail/mailboxes/{id}/forwarding` | mail write, own | `{"enabled", "to", "keep_copy", "replace"}` | the same object; `409` as above, or when the mailbox may not forward |
| `PUT` | `/api/mail/mailboxes/{id}/may-forward` | mail write, own | `{"allowed"}` (allowed by default) | `204`; whether the mailbox's filters may forward mail |
| `GET` | `/api/mail/mailboxes/{id}/apple-profile` | mail read, own | | An unsigned configuration profile (`application/x-apple-aspen-config`, attachment `<address>.mobileconfig`) with one IMAP/SMTP account (`com.apple.mail.managed`) for the mailbox; no password inside. Its identifiers are stable, so installing it again replaces it |
| `DELETE` | `/api/mail/mailboxes/{id}` | mail write, own | | `204`; the mail service deletes the stored mail (retried later if it cannot be reached) |
| `PUT` | `/api/mail/domains/{domain}/organisation` | mail write, own; the organisation must be yours (admins: any) | `{"organisation": "<id>"}`, `""` lets the domain stand alone | Mail domain. `403` for members when the organisation or its domains belong to someone else: only admins group domains of different owners (recorded as such) |
| `GET` | `/api/mail/organisations` | mail read | | list of Organisation you manage (admins: all), by name |
| `POST` | `/api/mail/organisations` | mail write | `{"name"}` | `201` Organisation, yours. `409` at 20 organisations (members) |
| `PATCH` | `/api/mail/organisations/{id}` | mail write, own | `{"name"}` | Organisation |
| `DELETE` | `/api/mail/organisations/{id}` | mail write, own | | `204`; its domains stand alone again |
| `GET` | `/api/mail/domains/{domain}/aliases` | mail read, own | | list of Alias |
| `POST` | `/api/mail/domains/{domain}/aliases` | mail write, own | `{"local_part", "targets": ["a@example.com", …], "sender_policy", "hide_from_directory"}` (the last two optional, default `anyone` and `false`; not for the catch-all) | `201` Alias. 1 to 50 targets; `409` if the address is a mailbox or an alias already |
| `GET` | `/api/mail/aliases` | mail read | | list of every Alias you may see |
| `GET` | `/api/mail/aliases/{id}` | mail read, own | | Alias |
| `PUT` | `/api/mail/aliases/{id}` | mail write, own | same as `POST`; missing `sender_policy` and `hide_from_directory` stay | Alias (stays in its domain) |
| `DELETE` | `/api/mail/aliases/{id}` | mail write, own | | `204` |

**Moving mail in and out** of a mailbox (see [[E-Mail|E-Mail]] and "Import and export" in [[E-Mail architecture|E-Mail-Architecture]]). The mailbox is addressed by its domain and local part. Importing and exporting need mail write (a password sign-in or an unrestricted `write` token) and owning the mail domain or being an admin; listing imports needs mail read (read tokens too). Another person's mailbox answers `404`. Passwords of other accounts are passed to the mail service only, never stored, logged, returned or written to the activity log. Each person and each mailbox may begin 10 checks, starts and uploads per hour.

**Import:** `{"id", "address", "kind", "state", "error", "host", "port", "username", "file_name", "file_size", "received", "check", "choices", "folders", "messages", "copied", "skipped", "failed", "bytes", "created", "started", "finished", "failures"}`. `kind` is `imap` or `file`; `state` is `uploading`, `checking`, `ready` (a file was read; start it), `waiting`, `running`, `paused` (the other server limits downloads), `interrupted` (the mail service restarted; IMAP imports need the password again), `done`, `failed` or `cancelled`. `folders` lists `{"remote", "local", "copied", "skipped", "failed", "done"}`; `failures` (only from `GET …/imports/{id}`, at most 1000) `{"folder", "date", "subject", "reason"}`. **Import check:** `{"folders": [{"remote", "local", "special_use", "messages", "bytes", "selected", "note"}], "messages", "bytes", "quota_bytes", "used_bytes", "quota_left", "skipped_entries", "own_export"}`; `note` is `junk`, `trash`, `starred`, `important`, `all-mail` or `too-many`; `quota_left` `-1` means no limit; `own_export` is true for a zip from "Download all mail" (the web interface then also selects Junk and Trash). **Choices:** `{"folders": ["INBOX", …], "include_junk", "include_trash"}`; with `folders`, exactly those are copied, otherwise the default ones plus junk and trash as the flags say.

| Method | Path below `/api/mail/domains/{domain}/mailboxes/{local_part}` | Who | Body | Answer |
|---|---|---|---|---|
| `POST` | `/imports/check` | mail write, own | `{"host", "port", "username", "password"}`; `port` 993 (default) or 143 | Import check, after signing in to the other account; nothing is copied. `502` with the other server's reason (sign-in refused, certificate, unreachable) |
| `POST` | `/imports` | mail write, own | the same plus `"choices"` | `201` Import (`waiting`) |
| `POST` | `/uploads` | mail write, own | `{"name", "size"}`, at most 20 GiB | `201` Import (`uploading`) |
| `PUT` | `/uploads/{id}?offset=N` | mail write, own | the bytes from `N` on, at most 256 MiB, `Content-Length` required | `{"received"}`. `N` must be what the server has (`received` of the import); otherwise `409`, then read the import and go on from there. After the last part the file is read (`checking`, then `ready` with `check`) |
| `POST` | `/imports/{id}/start` | mail write, own | `{"choices"}` for a `ready` file; `{"password"}` to continue a `paused` or `interrupted` IMAP import (empty body: default choices) | Import |
| `GET` | `/imports` | mail read, own | | list of Import, newest first, without `failures` |
| `GET` | `/imports/{id}` | mail read, own | | Import with `failures` |
| `DELETE` | `/imports/{id}` | mail write, own | | `204`; cancels an unfinished import (copied mail stays) or removes a finished one from the list |
| `GET` | `/export` | mail write, own | | `application/zip`, attachment `mail-<address>-<date>.zip`, streamed: `manifest.json` and one mboxrd file per folder. One export per mailbox at a time (`409`), four in the service (`429`). A connection cut before the end means the zip is incomplete |

A change that is saved but could not be sent to the mail service answers `502` with "saved, but the mail server could not be updated yet"; the panel keeps retrying. Without a mail service (`-mail-admin` not set) everything is stored, and `GET /api/mail/status` says the service is not set up.

The panel settings (`GET`/`PUT /api/settings`, admin) carry `preview_domain` (empty: versions have no preview address; see [[Configuration|Configuration]]). `GET /api/settings/checks` includes `preview_dns`: `{"name": "*.<preview domain>", "state", "found", "expected", "zone": null or {"connection_id", "connection_name", "zone", "record_name"}}`; `zone` is set when the record can be written into a zone of your DNS connections with `POST /api/settings/preview-domain/point` (admin; answers `{"changed": [Record set, …]}`).

The panel settings also carry `mail_host`, `mail_host_locked` and `postmaster_address`; see [[Configuration|Configuration]]. The spam checks are `mail_blocklists` (list of DNS blocklist zones, at most 10; `[]` turns them off; zones with a Spamhaus key, `<key>.….dq.spamhaus.net`, are answered as `••••.….dq.spamhaus.net`, and sending the masked name back keeps the stored zone), `mail_blocklists_default` (`true` uses the default list and ignores `mail_blocklists`), `mail_junk_score` (1–50, default 5) and `mail_greylisting` (default `false`). Omitted fields stay unchanged.

**Settings for mail programs (public, outside `/api/`).** Mail programs ask before they have a password, so these need no sign-in. They answer only for active mail domains, the same for every address (never whether a mailbox exists), and `404` for everything else:

| Method | Path | Host | Answer |
|---|---|---|---|
| `GET` | `/mail/config-v1.1.xml` | `autoconfig.<domain>` | client configuration 1.1 (Thunderbird): IMAP 993 SSL, SMTP 465 SSL and 587 STARTTLS, user name `%EMAILADDRESS%` |
| `GET` | `/.well-known/autoconfig/mail/config-v1.1.xml` | `<domain>` (a site of this panel) or `autoconfig.<domain>` | the same |
| `POST` | `/autodiscover/autodiscover.xml` | `autodiscover.<domain>` | Outlook "plain old XML" answer with IMAP and SMTP for the request's `EMailAddress`, which must be an address of `<domain>`. Requests over 8 KiB answer `413`, malformed ones `400`, requests for the mobile (ActiveSync) schema `404` |

Paths match regardless of case. Caddy passes them to the panel only for `autoconfig.`/`autodiscover.` names the panel has seen point to this server, and the well-known path on site domains that are active mail domains.

**The mailbox's own API (mail server name, outside the panel's API).** On `https://<mail server name>/account/api/` the mail service answers the mailbox's own page ([[E-Mail|E-Mail]], "Your mailbox's own page"). It does not take panel sessions or access tokens: a mailbox signs in with its address and mailbox password (app passwords are refused) and gets the cookie `__Host-ownpanel_mailbox`. Changing requests need `X-Ownpanel-Request: 1`; bodies are JSON, at most 16 KiB (256 KiB for filters); errors are `{"error": "…"}` in plain words. `401` means the session ended (sign in again).

**Me:** `{"address", "display_name", "used_bytes", "messages", "quota_bytes", "app_passwords": [{"id", "name", "created_at", "last_used_at", "last_used_network"}], "app_passwords_only", "app_passwords_available", "uses_known_days", "client": {"server", "imap_port", "smtp_port", "smtp_starttls_port", "user_name_is_address", "dav_url"}, "shared_with": [{"address", "display_name", "rights"}], "access": [{"address", "display_name", "shared", "rights", "folder"}]}`. `quota_bytes` `0` means no limit (`used_bytes` includes calendars and contacts); `last_used_at` is `null` when the app password was not used within `uses_known_days`; `dav_url` is where calendar and contacts programs connect (`https://<mail server name>/dav/`). `shared_with` are the mailboxes this one is shared with; `access` the mailboxes it may use (`shared`: a shared mailbox), with `folder` where their folders show up in mail programs (`Shared/<address>`). Shared mailboxes cannot sign in here.

| Method | Path below `/account/api` | Body | Answer |
|---|---|---|---|
| `POST` | `/session` | `{"address", "password"}` | Me, sets the cookie. `401` wrong address or password, `429` too many failures |
| `DELETE` | `/session` | | `204`, signs out |
| `GET` | `/me` | | Me |
| `PUT` | `/password` | `{"current_password", "new_password", "revoke_app_passwords"}` | Me. Other sessions of the mailbox end; with `revoke_app_passwords` every app password is revoked. If the password was changed but some app passwords could not be revoked, the answer is still `200` with Me and a `warning` saying so (they are still listed) |
| `PUT` | `/display-name` | `{"display_name"}` (`""` removes it) | Me |
| `PUT` | `/app-passwords-only` | `{"app_passwords_only"}` | Me |
| `POST` | `/app-passwords` | `{"name"}` | `201` `{"app_password", "password"}`; the password is returned only here. `409` at 50 app passwords |
| `DELETE` | `/app-passwords/{id}` | | `204` |
| `GET` | `/apple-setup` | | the setup for Apple devices, as in the panel, with the mail account and calendars and contacts |
| `PUT` | `/delegations` | `{"grants": [{"address", "rights": ["read", …]}]}`, the full list (`[]`: nobody) | Me. `400` with a sentence for yourself, an address that is not a mailbox of your organisation, a shared mailbox, no rights, an address listed twice, or more than 100. The panel's activity log shows `mail.mailbox.grants-changed` by the mailbox |
| `GET` | `/collections` | | `{"collections": [{"kind", "name", "display_name", "color", "objects", "bytes"}], "max_file_bytes"}`: the mailbox's calendars (`kind` `calendar`) and address books (`addressbook`); the default ones are created on first use |
| `GET` | `/collections/{kind}/{name}/export` | | the calendar as one `.ics` file or the address book as one `.vcf` file |
| `POST` | `/collections/{kind}/{name}/import` | the `.ics` or `.vcf` file itself (any `Content-Type`, at most 10 MB) | `{"added", "replaced", "skipped"}`: entries with a UID that is there already replace it. `400` the file can't be read, `413` too large, `507` the mailbox is full (with the counts so far and `error`) |
| `GET` | `/filters` | | `{"rules": {"managed", "out_of_office", "forwarding", "filters", "active_script", "script"}, "folders", "may_forward", "mailbox"}`. `managed` is false when filters written in a mail program are active: then `script` is their text and the settings are empty. With `?mailbox=<address>` (here and on the three `PUT` routes below) the settings are those of another mailbox the signed-in one has the Manage right on, for example a shared one; `mailbox` then names it, changes of the out-of-office reply and forwarding record the signed-in mailbox as `set_by`, and a mailbox without the right answers `404` |
| `PUT` | `/out-of-office` | `{"out_of_office": {"enabled", "subject", "text", "start", "end", "zone", "days"}, "replace"}` | as `GET /filters`. `409` when filters written in a mail program are active and `replace` is not set |
| `PUT` | `/forwarding` | `{"forwarding": {"enabled", "to", "keep_copy"}, "replace"}` | as `GET /filters`; `400` when the mailbox may not forward |
| `PUT` | `/filters` | `{"filters": [{"name", "match_any", "conditions": [{"field": "from"\|"to"\|"subject"\|"list", "op": "contains"\|"is", "value"}], "folder", "mark_read", "forward_to", "delete"}], "replace"}` (at most 50 rules of 10 conditions; body up to 256 KiB) | as `GET /filters` |
| `GET` | `/health` | | `{"status": "ok"}`, no sign-in |

For record routes, `{name}` is `@` for the zone itself, and `{type}` is for example `A` or `TXT`. See [[DNS|DNS]] for what is allowed.

Any other path under `/api/` answers `404` with `{"error": "No such API endpoint"}`.

## Example: publish from CI

```sh
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $OWNPANEL_TOKEN" \
  --data-binary @site.tar.gz \
  https://panel.example.com/api/sites/demo/deployments
```

Use a `write` token limited to the one site, so a leaked token can publish to that site but cannot create or delete sites.

The request is synchronous: the `201` answer comes after the new version is live, and its `published_release` (and `current_release`) is the new version's id. With `?live=false` the live version stays, and `preview_url` is where the new one can be opened. On GitHub Actions, the [[GitHub Action|GitHub-Action]] does this for you.
