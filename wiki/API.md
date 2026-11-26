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
| `409` | cannot be done in the current state: name taken, nothing to go back to, last admin, user still owns sites, setup already done, another import of the mailbox running |
| `413` | a file or part of an upload is too large |
| `429` | too many failed sign-in attempts, too many sign-ins at the same moment, Hetzner's request limit was reached, or too many mail imports in an hour |
| `502` | the change is saved but the web server could not be updated yet (the loop retries), or an outside service such as Hetzner or another mail server is unavailable or refused (for an import: its answer, never the password) |
| `507` | not enough free disk space on the server (mail import) |
| `500` | something went wrong on the server; the details are only in the log |

Responses carry `Cache-Control: no-store`. Request bodies that are JSON are limited to 1 MB; archives to 512 MB.

## Objects

**Site:** `{"name", "domains": [..], "owner", "current_release", "releases": [..]}`. `releases` lists version ids oldest first; `current_release` is empty until something is published.

**User:** `{"id", "name", "email", "role", "created_at"}`, role `"admin"` or `"member"`; `email` is `""` when the person has none.

**Token:** `{"id", "name", "access", "sites", "created_at", "last_used_at", "expires_at", "expired"}`. `sites` is `null` for all sites; `expires_at` is `null` for a token that never expires. Creating a token also returns `"token"` (the secret, shown once).

**Activity:** `{"id", "at", "actor", "action", "target", "detail"}`.

**DNS connection:** `{"id", "provider", "name", "owner", "created_at", "last_checked_at", "last_error", "can_write", "zone_count"}`. `can_write` and `zone_count` are `null` while unknown; the token is never returned.

**Record set:** `{"name", "type", "ttl", "values": [..], "locked"}`. `name` is `@` for the zone itself; `ttl` is `null` for the zone default; TXT values are plain text.

**Redirect:** `{"id", "owner", "source_domain", "source_path", "target", "code", "keep_path", "keep_query", "created_at"}`. `source_path` is `""` for a whole-domain redirect, otherwise `/exact` or a prefix `/path/*`; `code` is `301`, `302`, `307` or `308`. See [[Redirects|Redirects]].

Domains whose DNS is managed elsewhere: kept in your panel as domains of their own, with nothing at any DNS provider. Each person has their own list; another person's entry answers `404`, also for administrators.

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/domains` | anyone, not site-limited | | list of Domain, by name |
| `POST` | `/api/domains` | password sign-in or unrestricted write | `{"name": "example.com"}` | Domain. Only registrable domains (no subdomains, no IP addresses, non-ASCII in `xn--` form); `400` otherwise. Adding it again is fine |
| `DELETE` | `/api/domains/{domain}` | same | | `{"name", "used_by"}`. Removes only the entry; nothing changes at any provider. `used_by` lists what of yours still uses a name in the domain, so it stays in the panel through that. `404` if it is not in your list |

**Domain:** `{"name", "created_at", "used_by"}`; `used_by` as for Zone below.

**Zone:** `{"connection_id", "connection_name", "name", "read_only", "nameservers": [..]}`. `nameservers` are the provider's nameservers to enter at the domain's registrar.

**Mail domain:** `{"domain", "owner", "active", "activated_at", "checked_at", "problem", "mailboxes", "aliases", "dkim_selector", "dkim_record", "created_at"}`. `active` turns true once the domain's MX record (asked at its authoritative nameservers) points to the mail host; only then is mail received for it. `problem` says why it is not active yet. `dkim_record` is the public TXT value for `<dkim_selector>._domainkey.<domain>`; the private key is never returned.

**Mailbox:** `{"id", "address", "domain", "local_part", "owner", "quota_bytes", "send_per_hour", "send_per_day", "created_at", "usage"}`. `quota_bytes` `0` means no limit. `usage` is `{"used_bytes", "messages", "blocked", "blocked_reason", "blocked_at"}` as reported by the mail service, or `null` when it could not be asked. Password hashes are never returned.

**Alias:** `{"id", "address", "domain", "local_part", "catch_all", "targets": [..], "owner", "created_at"}`. Local part `*` is the domain's catch-all.

**Mail status:** `{"mail_host", "service_set_up", "reachable", "in_sync", "problem", "listeners": [{"name", "address", "listening", "error"}], "certificate_expires", "queue_length", "client": {"server", "imap_port", "smtp_port", "smtp_starttls_port", "user_name_is_address"}}`.

**Mail records:** `{"domain", "mail_host", "records": [{"kind", "type", "name", "fqdn", "want", "found", "status", "note"}], "can_apply", "connection_id", "zone", "apply_note"}`. `kind` is `mx`, `spf`, `dkim`, `dmarc`, `srv-submissions`, `srv-imaps`, `autoconfig` or `autodiscover`; the last two are CNAMEs to the mail host with `"optional": true` (recommended, mail works without them), judged by where the name leads: `ok` when it resolves to the server's addresses only (a CNAME to the mail host or A/AAAA records), `missing`, `different` (it leads elsewhere; `found` lists those addresses) or `unknown` (also when the server's addresses are not set). Applying creates the CNAME where the name is unused, replaces a CNAME that leads elsewhere and skips names with other records; `found` are the values the domain's authoritative nameservers answer; `status` is `ok`, `missing`, `different`, `conflict` (needs a person, never changed automatically: several SPF or DMARC records, SPF with `+all`/`?all` or more than 10 lookups) or `unknown` (the lookup failed). For SPF, `want` is the existing record with `mx` added; an existing DMARC record is `ok` whatever it says. `can_apply` is true when the domain lies in a zone of exactly one of **your own** DNS connections with write access (never someone else's, also not for admins) and you may write; `connection_id` and `zone` then name it and `name` is relative to that zone, otherwise `connection_id` is `0`, `zone` is empty, `name` is relative to the mail domain and `apply_note` says why.

**Mail record changes:** `{"changes": [{"kind", "type", "name", "action", "old", "new", "needs_confirm", "note", "optional"}], "applied"}`. `action` is `create`, `replace`, `add` (to other TXT values, which stay), `keep` or `skip`. Only replacing MX records that do not point to the mail host has `needs_confirm` (mail would move here). `applied` is `false` only when such a change was not confirmed; nothing was written then.

**Mail health:** `{"mail_host", "checks": [{"id", "state", "found", "expected", "detail", "can_apply"}]}` with the checks `service`, `mail_host_dns`, `mail_host_spf`, `ptr`, `outbound_port`, `certificate`, `listeners`, `postmaster`, `blocklists` in this order; `state` is `ok`, `warning`, `error` or `unknown` (a lookup or the mail service could not be asked). IPv4 addresses without a PTR naming `mail_host` are an error, IPv6 ones a warning. The outbound port check is reused for 10 minutes. `mail_host_spf` looks at the SPF record of `mail_host` itself (bounces and SRS-forwarded mail use it as sender domain): `expected` is `v=spf1 a -all`, or the existing record with `a` added; a missing or unmerged record is a `warning`, an SPF record that cannot be merged safely a `warning` without `expected`. `can_apply` is true (only on this check) when an admin's own DNS connection holds the zone of `mail_host`.

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
| `POST` | `/api/sites/{name}/deployments` | write, own site | raw `.zip` or `.tar.gz` body | `201` Site |
| `POST` | `/api/sites/{name}/rollback` | write, own site | optional `{"release": "<id>"}`; no body means the previous version | Site |
| `GET` | `/api/sites/{name}/icon` | anyone, own site | | the icon of the live version: a `<link rel="icon">` (or `shortcut icon`, `apple-touch-icon`) in `index.html` pointing at a file in the same version or a `data:` URL up to 64 KB, else `favicon.svg`, `favicon.png`, `favicon.ico` or `apple-touch-icon.png`; PNG, ICO, SVG, WebP, GIF or JPEG up to 256 KB. Nothing is fetched from elsewhere. `404` if there is none |
| `POST` | `/api/uploads/{id}` | open (upload link) | raw archive body | `201` Site; `404` for unknown, used or expired links |

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
| `POST` | `/api/mail/domains/{domain}/mailboxes` | mail write, own | `{"local_part", "password", "quota_bytes", "send_per_hour", "send_per_day"}` (limits optional) | `201` Mailbox. Defaults: 5 GiB, 100 per hour, 500 per day; members can only choose lower values. `409` if the address is taken (also by an alias) or you have 200 mailboxes |
| `GET` | `/api/mail/mailboxes` | mail read | | list of every Mailbox you may see |
| `GET` | `/api/mail/mailboxes/{id}` | mail read, own | | Mailbox |
| `PATCH` | `/api/mail/mailboxes/{id}` | mail write, own; only admins raise values | `{"quota_bytes", "send_per_hour", "send_per_day"}`, missing fields stay | Mailbox |
| `POST` | `/api/mail/mailboxes/{id}/password` | mail write, own | `{"password"}` (10 to 200 characters) | `204` |
| `POST` | `/api/mail/mailboxes/{id}/unblock` | admin | | `204`; lifts a sending block of the mail service |
| `GET` | `/api/mail/mailboxes/{id}/apple-profile` | mail read, own | | An unsigned configuration profile (`application/x-apple-aspen-config`, attachment `<address>.mobileconfig`) with one IMAP/SMTP account (`com.apple.mail.managed`) for the mailbox; no password inside. Its identifiers are stable, so installing it again replaces it |
| `DELETE` | `/api/mail/mailboxes/{id}` | mail write, own | | `204`; the mail service deletes the stored mail (retried later if it cannot be reached) |
| `GET` | `/api/mail/domains/{domain}/aliases` | mail read, own | | list of Alias |
| `POST` | `/api/mail/domains/{domain}/aliases` | mail write, own | `{"local_part", "targets": ["a@example.com", …]}` | `201` Alias. 1 to 50 targets; `409` if the address is a mailbox or an alias already |
| `GET` | `/api/mail/aliases` | mail read | | list of every Alias you may see |
| `GET` | `/api/mail/aliases/{id}` | mail read, own | | Alias |
| `PUT` | `/api/mail/aliases/{id}` | mail write, own | same as `POST` | Alias (stays in its domain) |
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

The panel settings (`GET`/`PUT /api/settings`, admin) also carry `mail_host`, `mail_host_locked` and `postmaster_address`; see [[Configuration|Configuration]]. The spam checks are `mail_blocklists` (list of DNS blocklist zones, at most 10; `[]` turns them off; zones with a Spamhaus key, `<key>.….dq.spamhaus.net`, are answered as `••••.….dq.spamhaus.net`, and sending the masked name back keeps the stored zone), `mail_blocklists_default` (`true` uses the default list and ignores `mail_blocklists`), `mail_junk_score` (1–50, default 5) and `mail_greylisting` (default `false`). Omitted fields stay unchanged.

**Settings for mail programs (public, outside `/api/`).** Mail programs ask before they have a password, so these need no sign-in. They answer only for active mail domains, the same for every address (never whether a mailbox exists), and `404` for everything else:

| Method | Path | Host | Answer |
|---|---|---|---|
| `GET` | `/mail/config-v1.1.xml` | `autoconfig.<domain>` | client configuration 1.1 (Thunderbird): IMAP 993 SSL, SMTP 465 SSL and 587 STARTTLS, user name `%EMAILADDRESS%` |
| `GET` | `/.well-known/autoconfig/mail/config-v1.1.xml` | `<domain>` (a site of this panel) or `autoconfig.<domain>` | the same |
| `POST` | `/autodiscover/autodiscover.xml` | `autodiscover.<domain>` | Outlook "plain old XML" answer with IMAP and SMTP for the request's `EMailAddress`, which must be an address of `<domain>`. Requests over 8 KiB answer `413`, malformed ones `400`, requests for the mobile (ActiveSync) schema `404` |

Paths match regardless of case. Caddy passes them to the panel only for `autoconfig.`/`autodiscover.` names the panel has seen point to this server, and the well-known path on site domains that are active mail domains.

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

The request is synchronous: the `201` answer comes after the new version is live, and its `current_release` is the new version's id. On GitHub Actions, the [[GitHub Action|GitHub-Action]] does this for you.
