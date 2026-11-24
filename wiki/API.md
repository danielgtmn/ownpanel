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
| `POST` | `/api/uploads/{id}` | open (upload link) | raw archive body | `201` Site; `404` for unknown, used or expired links |

### Users and activity

| Method | Path | Who | Request | Response |
|---|---|---|---|---|
| `GET` | `/api/users` | admin | | list of User |
| `POST` | `/api/users` | admin | `{"name", "password", "role"}` | `201` User |
| `PUT` | `/api/users/{id}` | admin | `{"role"}` and/or `{"password"}` | User |
| `DELETE` | `/api/users/{id}` | admin | | `204`; `409` for the last admin or a user who owns sites |
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
