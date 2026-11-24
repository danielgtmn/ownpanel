# ownpanel

An open-source hosting panel. Current state: a **spike** that proves the core
idea for static web hosting, with users, access tokens, a web interface and
DNS records at Hetzner. No e-mail yet.

## How it works

- **Caddy** serves the sites and obtains certificates. It runs as its own
  process; its admin API is a Unix socket only ownpaneld can reach.
- **ownpaneld** (Go) keeps the panel's data in SQLite (`ownpanel.db`: users,
  sessions, access tokens, sites and their domains, DNS connections, activity
  log), builds a
  complete Caddy config from it and loads it through Caddy's admin API. It
  re-syncs in the background, so a restarted Caddy gets its sites back.
- A deployment is a `.zip` or `tar.gz` extracted into an immutable release directory;
  going live or rolling back is an atomic symlink switch.

```
cmd/ownpaneld        daemon entry point (serve and backup commands, flags, wiring)
internal/panel     the panel's operations and all permission checks; every front door calls these
internal/store     SQLite database and its migrations (internal/store/migrations)
internal/caddy     Caddy config builder and admin API client
internal/deploy    releases, activation, rollback
internal/dns       DNS provider interface, zone lookup, "does the domain point here?"; dns/hetzner is the Hetzner Cloud client
internal/secrets   encryption of stored secrets (DNS tokens) with the key in <data>/secret.key
internal/api       HTTP API: authentication, then a thin adapter over internal/panel
internal/mcp       MCP server (/mcp): the same operations as tools for AI assistants
internal/backup    backup archives
internal/webui     serves the web interface (embedded with `-tags webui`)
web/               web interface (React, built with Vite)
deploy/            systemd units and installer for a Linux server
scripts/           local dev runner and end-to-end check
```

## Try it locally

```sh
scripts/dev.sh                 # Caddy + ownpaneld on ports 8080/8443, self-signed
INSECURE=1 scripts/smoke.sh    # in a second terminal: full end-to-end check
```

On a fresh `./data` the smoke test performs the first-run setup itself and
prints the admin password it chose. Later runs need `ADMIN_NAME` and
`ADMIN_PASSWORD`, or an access token in `OWNPANEL_TOKEN`.

## First run

While no user exists, ownpaneld writes a one-time **setup code** to
`<data>/setup-code` (readable only by the daemon's user) and logs where it
is. Open the panel, enter the code and choose the first admin's name and
password. The file is deleted afterwards and setup is closed for good.

## Users, roles and access tokens

- **Admins** see and manage all sites, users and the activity log.
- **Members** see and manage only the sites they created.
- Everyone can change their own password and create **access tokens** for
  scripts and CI (`Authorization: Bearer op_…`). A token is shown once. It has
  `read` or `write` access and either covers all of the user's sites or a
  chosen list (a limited token can publish to those sites but not create or
  delete sites). It never has more rights than its user has at that moment.
- Sign-ins last 30 days. After 5 failed sign-ins for a name from one address
  within 15 minutes, further attempts are refused until the window passes.
- Every change is recorded in the activity log, with the token's name when a
  token was used.

## Web interface

The daemon serves the web app on the same address as the API; the app signs
in with name and password. For development run `scripts/dev.sh` and
`npm run dev` in `web/`. For production `make build` builds the app and
embeds it into `bin/ownpaneld` (`make build-linux` for a server). A plain
`go build` leaves the app out and only serves the API.

## API

JSON in and out; errors are `{"error": "Readable sentence"}`. Requests are
authenticated by an access token or by the session cookie. Changing requests
made with the cookie must send `X-Ownpanel-Request: 1`.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/session` | setup needed? who is signed in? |
| `POST` | `/api/setup` | first admin, body `{"setup_code", "name", "password"}` |
| `POST` | `/api/login`, `/api/logout` | sign in (body `{"name", "password"}`) and out |
| `PUT` | `/api/account/password` | body `{"current", "new"}`; ends your other sessions |
| `GET` `POST` | `/api/tokens` | your access tokens; create with `{"name", "access": "read"\|"write", "sites": [..] \| null}` |
| `DELETE` | `/api/tokens/{id}` | revoke a token |
| `GET` | `/api/sites` | list sites |
| `GET` `PUT` | `/api/sites/{name}` | show; create or update with `{"domains": ["example.com"]}` |
| `DELETE` | `/api/sites/{name}` | remove site and its files |
| `POST` | `/api/sites/{name}/deployments` | upload a `.zip` or `tar.gz` (max 512 MB); its root becomes the web root (a single top-level folder is unwrapped) |
| `POST` | `/api/uploads/{id}` | upload a `.zip` or `tar.gz` through a one-time upload link (the link is the credential; see MCP) |
| `POST` | `/api/sites/{name}/rollback` | previous release, or body `{"release": "<id>"}` |
| `GET` `POST` | `/api/users` | admins: list, create with `{"name", "password", "role"}` |
| `PUT` `DELETE` | `/api/users/{id}` | admins: change `{"role"}` and/or `{"password"}`, delete |
| `GET` | `/api/activity?limit=50` | admins: newest activity first |
| `GET` | `/api/server` | `{"ips": [..]}`: the addresses given with `-server-ip` |
| `GET` `POST` | `/api/dns/connections` | DNS connections (yours; admins see all); add with `{"provider": "hetzner", "name", "token"}` |
| `POST` | `/api/dns/connections/{id}/check` | list the zones again; the outcome is in `last_error` |
| `DELETE` | `/api/dns/connections/{id}` | remove a connection (admins: anyone's) |
| `GET` | `/api/dns/zones` | `{"zones": [..], "problems": [..]}` across your connections |
| `GET` | `/api/dns/connections/{id}/zones/{zone}/records` | record sets of a zone (`ttl` null = zone default) |
| `PUT` `DELETE` | `/api/dns/connections/{id}/zones/{zone}/records/{name}/{type}` | set with `{"ttl": 3600 \| null, "values": [..]}`, or delete; `name` is `@` for the zone itself |
| `GET` | `/api/sites/{name}/domains/status` | per domain: `points_here`, `points_elsewhere`, `no_record` or `lookup_failed`, and the zone "point here" would use |
| `POST` | `/api/sites/{name}/domains/{domain}/point` | write A/AAAA records for the domain; `{"changed": [..]}` |

Tokens and passwords can only be managed when signed in with a password.

The daemon listens on `127.0.0.1:8600`. To reach the panel over HTTPS, start
it with `-panel-domain panel.example.com` and point that domain at the
server: Caddy then serves the panel on it with an automatic certificate, like
any site.

## MCP

ownpaneld speaks the Model Context Protocol on `/mcp`, so AI assistants can
manage sites: create them, publish files, go back to an earlier version.
It uses the same operations and permission checks as the API. Connect with an
access token (create one under your account; `read` tokens can only look):

```sh
claude mcp add --transport http ownpanel https://panel.example.com/mcp \
  --header "Authorization: Bearer op_…"
```

Other clients need the same two things: the address and the header
`Authorization: Bearer op_…`. Only access tokens work, never the browser
session, and the token's rights apply: a read-only token cannot change
anything, a token limited to some sites only sees and publishes those.

| Tool | Does |
|---|---|
| `list_sites`, `get_site` | sites with domains, owner, live version and all versions (newest first, with time) |
| `create_site`, `set_site_domains` | create a site; replace its list of domains |
| `delete_site` | delete a site with all files; needs `confirm_name` equal to the name |
| `publish_files` | publish files given directly (text or base64; up to 2000 files, 10 MB) as a new live version |
| `create_upload_link` | one-time link and `curl` command (valid 15 minutes) to upload a `.zip` or `tar.gz` up to 512 MB |
| `make_version_live` | switch to a kept version; without one, back to the previous |
| `check_site_domains` | do the domains point at this server? |

Without `-panel-domain`, `/mcp` is only reachable on the daemon's own
address (the Host must be local). Upload links are built on the panel domain,
else on the address of the request. They live in memory, so a restart
invalidates them. Activity log entries show the token that was used.

## DNS

ownpanel can show whether the domains of a site point at the server, set them
up with one click ("point here"), and edit the DNS records of zones hosted at
**Hetzner** (the DNS in the Hetzner Console, API `api.hetzner.cloud`; the old
`dns.hetzner.com` API was shut down in May 2026 and is not supported).

**Server addresses.** Start ownpaneld with the public addresses domains should
point at, IPv4 and/or IPv6, repeated or comma-separated:

```sh
ownpaneld -server-ip 203.0.113.5 -server-ip 2001:db8::5
ownpaneld -server-ip 203.0.113.5,2001:db8::5
```

They must be public addresses unless `-tls` is `internal` or `off`. Without
them the domain status and "point here" answer that the server's public
address is not configured.

**Connecting Hetzner.** In the Hetzner Console open the project that holds
your zones, go to Security → API tokens and create a token with **Read &
Write** access. A token covers exactly one project; for zones in several
projects add one connection per project. The token is checked by listing
the zones before it is saved. A Read-only token is accepted, but every change
is then refused by Hetzner, and the connection shows that.

- Connections belong to the person who added them. Admins see everyone's
  connections (name and owner, never the token) and can remove them, but can
  only read and change records through their own.
- Adding, checking and removing connections needs a password sign-in or an
  access token with write access to all sites. `read` tokens can list zones
  and records. Tokens limited to some sites can only see the domain status
  of their sites and use "point here" for them.
- "Point here" looks for the zone of the domain across your own connections
  (the most specific zone wins; if the same zone is in two connections it
  refuses until you remove one). It writes A and AAAA records with the server
  addresses (TTL 300) and removes an address record of a family the server
  has no address in. It never removes a CNAME; it asks you to do that.
- Zone lists are kept for 60 seconds and domain lookups for 30 seconds, to
  stay well below Hetzner's limit of 3600 requests per hour.
- Changes are recorded in the activity log (`dns.connect`, `dns.disconnect`,
  `dns.record.set`, `dns.record.delete`, `dns.point`); tokens never are, and
  TXT values longer than 60 characters are left out.

**Secrets.** API tokens are stored in the database encrypted (AES-256-GCM)
with a key in `<data>/secret.key`, created on first start (mode 0600). The
API never returns a token after it was added. Without that file the stored
tokens cannot be used; connections then have to be added again.

## Install on a server

Build `ownpaneld` and `caddy` for Linux, copy them together with `deploy/*` to
the server and run `install.sh` as root (it can be run again to upgrade).
Then read the setup code with `sudo cat /var/lib/ownpanel/setup-code` and
create the first admin. Options go into `/etc/ownpanel/ownpaneld.env`
(`OWNPANELD_ARGS`), e.g. `-server-ip` with the server's public addresses (see
DNS). Point a domain at the server, then create a site for it; the
certificate is requested automatically.

## Backup and restore

```sh
sudo ownpaneld backup -data /var/lib/ownpanel -out /root/ownpanel-$(date +%F).tar.gz
```

The archive (mode 0600) holds a consistent copy of the database, all site
releases, Caddy's certificates and `secret.key`. It can be made while
everything runs; run it as root so the certificates are readable.

**The backup contains secrets**: with `secret.key` next to the database,
anyone who has the archive can decrypt the stored DNS API tokens (and use
the certificates' private keys). Keep it as safe as the server itself.

To restore, stop both services, remove the old database files, unpack and
fix the owners:

```sh
systemctl stop ownpaneld ownpanel-caddy
rm -f /var/lib/ownpanel/ownpanel.db /var/lib/ownpanel/ownpanel.db-wal /var/lib/ownpanel/ownpanel.db-shm
tar -xzf ownpanel-2026-01-31.tar.gz -C /var/lib/ownpanel
chown -R ownpanel:ownpanel /var/lib/ownpanel/ownpanel.db /var/lib/ownpanel/sites /var/lib/ownpanel/secret.key
chown -R caddy:caddy /var/lib/ownpanel/caddy
systemctl start ownpanel-caddy ownpaneld
```

The leftover `-wal` and `-shm` files must go: SQLite would otherwise apply
them to the restored database.

## Known gaps

- No per-site system users or quotas; all site files belong to one user.
- Sign-in throttling is kept in memory and trusts `X-Forwarded-For` from
  local connections, so it resets on restart and local users can get around it.

## Documentation

The full documentation is in the [wiki](https://github.com/danielgtmn/ownpanel/wiki).
It is generated from the `wiki/` folder of this repository; edit it there.

## License

ownpanel is licensed under the [GNU Affero General Public License v3.0](LICENSE).
