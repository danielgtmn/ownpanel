# API

The panel's web interface uses the same HTTP API that scripts can use. It is served on the panel's address under `/api/`. Requests and responses are JSON, except archive uploads, which send the raw file as the request body.

## Authentication

| Client | How |
|---|---|
| Programs (scripts, CI) | `Authorization: Bearer op_…` with an access token |
| Browsers | the session cookie `ownpanel_session`, set by `POST /api/login` or `POST /api/setup` |

- Access tokens are described in [[Users and Access|Users-and-Access]].
- If an `Authorization` header is present it must be `Bearer <token>`; a wrong token is refused right away with `401`. Otherwise the cookie is used. A stale cookie just means nobody is signed in.
- **Cookie requests that change something** (anything but `GET` and `HEAD`) must send the header `X-Ownpanel-Request: 1`, otherwise they get `403`. Token requests do not need it.
- If an `Origin` header is sent on a changing request without a token, its host must equal the request's `Host`, otherwise `403` ("requests from other web sites are not allowed"). The API never allows cross-origin requests (no CORS).
- Tokens and passwords can only be managed when signed in with a password, not with a token.
- Some routes are open: `GET /api/session`, `POST /api/setup` (needs the setup code), `POST /api/login`, `POST /api/logout`, and `POST /api/uploads/{id}` (the link is the credential).

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
| `409` | cannot be done in the current state: name taken, nothing to go back to, last admin, user still owns sites, setup already done |
| `429` | too many failed sign-in attempts, or Hetzner's request limit was reached |
| `502` | the change is saved but the web server could not be updated yet (the loop retries), or an outside service such as Hetzner is unavailable |
| `500` | something went wrong on the server; the details are only in the log |

Responses carry `Cache-Control: no-store`. Request bodies that are JSON are limited to 1 MB; archives to 512 MB.

## Objects

**Site:** `{"name", "domains": [..], "owner", "current_release", "releases": [..]}`. `releases` lists version ids oldest first; `current_release` is empty until something is published.

**User:** `{"id", "name", "role", "created_at"}`, role `"admin"` or `"member"`.

**Token:** `{"id", "name", "access", "sites", "created_at", "last_used_at"}`. `sites` is `null` for all sites. Creating a token also returns `"token"` (the secret, shown once).

**Activity:** `{"id", "at", "actor", "action", "target", "detail"}`.

**DNS connection:** `{"id", "provider", "name", "owner", "created_at", "last_checked_at", "last_error", "can_write", "zone_count"}`. `can_write` and `zone_count` are `null` while unknown; the token is never returned.

**Record set:** `{"name", "type", "ttl", "values": [..], "locked"}`. `name` is `@` for the zone itself; `ttl` is `null` for the zone default; TXT values are plain text.

**Redirect:** `{"id", "owner", "source_domain", "source_path", "target", "code", "keep_path", "keep_query", "created_at"}`. `source_path` is `""` for a whole-domain redirect, otherwise `/exact` or a prefix `/path/*`; `code` is `301`, `302`, `307` or `308`. See [[Redirects|Redirects]].

**Zone:** `{"connection_id", "connection_name", "name", "read_only", "nameservers": [..]}`. `nameservers` are the provider's nameservers to enter at the domain's registrar.

**Mail domain:** `{"domain", "owner", "active", "activated_at", "checked_at", "problem", "mailboxes", "aliases", "dkim_selector", "dkim_record", "created_at"}`. `active` turns true once the domain's MX record (asked at its authoritative nameservers) points to the mail host; only then is mail received for it. `problem` says why it is not active yet. `dkim_record` is the public TXT value for `<dkim_selector>._domainkey.<domain>`; the private key is never returned.

**Mailbox:** `{"id", "address", "domain", "local_part", "owner", "quota_bytes", "send_per_hour", "send_per_day", "created_at", "usage"}`. `quota_bytes` `0` means no limit. `usage` is `{"used_bytes", "messages", "blocked", "blocked_reason", "blocked_at"}` as reported by the mail service, or `null` when it could not be asked. Password hashes are never returned.

**Alias:** `{"id", "address", "domain", "local_part", "catch_all", "targets": [..], "owner", "created_at"}`. Local part `*` is the domain's catch-all.

**Mail status:** `{"mail_host", "service_set_up", "reachable", "in_sync", "problem", "listeners": [{"name", "address", "listening", "error"}], "certificate_expires", "queue_length", "client": {"server", "imap_port", "smtp_port", "smtp_starttls_port", "user_name_is_address"}}`.

**Mail records:** `{"domain", "mail_host", "records": [{"kind", "type", "name", "fqdn", "want", "found", "status", "note"}], "can_apply", "connection_id", "zone", "apply_note"}`. `kind` is `mx`, `spf`, `dkim`, `dmarc`, `srv-submissions` or `srv-imaps`; `found` are the values the domain's authoritative nameservers answer; `status` is `ok`, `missing`, `different`, `conflict` (needs a person, never changed automatically: several SPF or DMARC records, SPF with `+all`/`?all` or more than 10 lookups) or `unknown` (the lookup failed). For SPF, `want` is the existing record with `mx` added; an existing DMARC record is `ok` whatever it says. `can_apply` is true when the domain lies in a zone of exactly one of **your own** DNS connections with write access (never someone else's, also not for admins) and you may write; `connection_id` and `zone` then name it and `name` is relative to that zone, otherwise `connection_id` is `0`, `zone` is empty, `name` is relative to the mail domain and `apply_note` says why.

**Mail record changes:** `{"changes": [{"kind", "type", "name", "action", "old", "new", "needs_confirm", "note"}], "applied"}`. `action` is `create`, `replace`, `add` (to other TXT values, which stay), `keep` or `skip`. Only replacing MX records that do not point to the mail host has `needs_confirm` (mail would move here). `applied` is `false` only when such a change was not confirmed; nothing was written then.

**Mail health:** `{"mail_host", "checks": [{"id", "state", "found", "expected", "detail"}]}` with the checks `service`, `mail_host_dns`, `ptr`, `outbound_port`, `certificate`, `listeners`, `postmaster` in this order; `state` is `ok`, `warning`, `error` or `unknown` (a lookup or the mail service could not be asked). IPv4 addresses without a PTR naming `mail_host` are an error, IPv6 ones a warning. The outbound port check is reused for 10 minutes.

## Routes

"Anyone" means any authenticated person or token. "Write" means a token needs `write` access; limited tokens work only on their sites. "Admin" means the admin role with a password sign-in or an unrestricted `write` token. Details are in [[Users and Access|Users-and-Access]].

### Session and account

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/session` | open | | `{"setup_required": bool, "user": User or null, "via": "session" or "token" or null}` |
| `POST` | `/api/setup` | open, needs setup code | `{"setup_code", "name", "password"}` | `201` User; sets the session cookie. `409` once set up |
| `POST` | `/api/login` | open | `{"name", "password"}` | User; sets the cookie. `401` wrong credentials, `429` throttled |
| `POST` | `/api/logout` | open | | `204`; clears the cookie |
| `PUT` | `/api/account/password` | password sign-in | `{"current", "new"}` | `204`; ends your other sessions |

### Access tokens

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/tokens` | password sign-in | | list of Token (your own) |
| `POST` | `/api/tokens` | password sign-in | `{"name", "access": "read" or "write", "sites": null or ["name", …]}` | `201` Token with `token` |
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
| `POST` | `/api/users` | admin | `{"name", "password", "role"}` | `201` User |
| `PUT` | `/api/users/{id}` | admin | `{"role"}` and/or `{"password"}` | User |
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
| `GET` | `/api/dns/zones` | anyone, not site-limited | | `{"zones": [{"connection_id", "connection_name", "name", "read_only"}], "problems": [{"connection_id", "connection_name", "error"}]}` across your connections |
| `GET` | `/api/dns/connections/{id}/zones/{zone}/records` | anyone, not site-limited, your own connection | | list of Record set |
| `PUT` | `/api/dns/connections/{id}/zones/{zone}/records/{name}/{type}` | write, unrestricted, your own connection | `{"ttl": 3600 or null, "values": [..]}` | Record set as written |
| `DELETE` | `/api/dns/connections/{id}/zones/{zone}/records/{name}/{type}` | same | | `204`; deleting a missing record set is fine |
| `GET` | `/api/sites/{name}/domains/status` | anyone, own site | | list of `{"domain", "state", "found": [..], "zone": null or {"connection_id", "connection_name", "zone", "record_name"}}`; `state` is `points_here`, `points_elsewhere`, `no_record` or `lookup_failed`. `409` if no `-server-ip` is set |
| `POST` | `/api/sites/{name}/domains/{domain}/point` | write, own site | | `{"changed": [Record set, …]}` |
| `GET` | `/api/dns/domain-status?domain=<domain>` | anyone; a domain of a site you may see or of a domain redirect you may see | | one element of the site domain status list above. `404` for other domains |
| `POST` | `/api/dns/point` | write; same domains | `{"domain": "www.example.com"}` | `{"changed": [Record set, …]}`, like the site route |
| `POST` | `/api/dns/connections/{id}/zones` | password sign-in or unrestricted write, your own connection | `{"name": "example.com"}` | `201` Zone. Only registrable domains (no subdomains). `409` if it exists already |
| `DELETE` | `/api/dns/connections/{id}/zones/{zone}` | same | `{"confirm": "<zone name>"}` | `204`. `400` if `confirm` does not match; `409` while a site, redirect or the panel domain (of anyone) uses a domain in the zone, listing them, or if the zone is protected at the provider |

Entries of `GET /api/dns/zones` are Zone objects, with `nameservers`.

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
| `GET` | `/api/mail/domains` | mail read | | list of Mail domain you may see, sorted by name |
| `POST` | `/api/mail/domains` | mail write; members only for a domain of their site or domain redirect, or in a zone of their own DNS connection; admins any domain | `{"domain": "example.com"}` | `201` Mail domain. `400` if another person's site or redirect uses the domain (not naming it) or for the mail host itself, `403` for a domain you do not control, `409` if e-mail is already enabled for it or you have 20 mail domains |
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
| `DELETE` | `/api/mail/mailboxes/{id}` | mail write, own | | `204`; the mail service deletes the stored mail (retried later if it cannot be reached) |
| `GET` | `/api/mail/domains/{domain}/aliases` | mail read, own | | list of Alias |
| `POST` | `/api/mail/domains/{domain}/aliases` | mail write, own | `{"local_part", "targets": ["a@example.com", …]}` | `201` Alias. 1 to 50 targets; `409` if the address is a mailbox or an alias already |
| `GET` | `/api/mail/aliases` | mail read | | list of every Alias you may see |
| `GET` | `/api/mail/aliases/{id}` | mail read, own | | Alias |
| `PUT` | `/api/mail/aliases/{id}` | mail write, own | same as `POST` | Alias (stays in its domain) |
| `DELETE` | `/api/mail/aliases/{id}` | mail write, own | | `204` |

A change that is saved but could not be sent to the mail service answers `502` with "saved, but the mail server could not be updated yet"; the panel keeps retrying. Without a mail service (`-mail-admin` not set) everything is stored, and `GET /api/mail/status` says the service is not set up.

The panel settings (`GET`/`PUT /api/settings`, admin) also carry `mail_host`, `mail_host_locked` and `postmaster_address`; see [[Configuration|Configuration]].

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
