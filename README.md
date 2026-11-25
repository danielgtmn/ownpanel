# ownpanel

An open-source hosting panel. Current state: a **spike** that proves the core
idea for static web hosting, with users, access tokens, a web interface and
DNS records at Hetzner, and e-mail with its own mail server.

## How it works

- **Caddy** serves the sites and obtains certificates. It runs as its own
  process; its admin API is a Unix socket only ownpaneld can reach.
- **ownpanel-mail** is the mail server (`ownpaneld mail`), a service of its
  own with its own user; ownpaneld pushes its configuration over a local
  socket. See the wiki pages E-Mail and E-Mail architecture.
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
internal/api       HTTP API: authentication, then a thin adapter over internal/panel
internal/mcp       MCP server (/mcp): the same operations as tools for AI assistants
internal/backup    backup archives
internal/webui     serves the web interface (embedded with `-tags webui`)
web/               web interface (React, built with Vite)
deploy/            systemd units and Caddy bootstrap for a Linux server
install.sh         server installer (install, upgrade, uninstall)
scripts/           local dev runner, end-to-end check, release packaging
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

Then the setup steps ask for the panel's domain, the server's public
addresses and an optional certificate e-mail; they are stored in the panel
and applied at once. Until then the panel is also served on
`https://<server address>/` with a self-signed certificate; once the setup is
finished with a domain, that address redirects to the domain. The flags
`-panel-domain`, `-server-ip` and `-acme-email` still work and win (shown as
locked in the panel).

## Users, roles and access tokens

- **Admins** see and manage all sites, users and the activity log.
- **Members** see and manage only the sites they created.
- People sign in with their name or an optional **e-mail address**. With an
  address stored and the panel able to send e-mail through its own mail
  server, a forgotten password can be reset with a link that works once and
  for an hour.
- Everyone can change their own password and create **access tokens** for
  scripts and CI (`Authorization: Bearer op_…`). A token is shown once. It has
  `read` or `write` access and either covers all of the user's sites or a
  chosen list (a limited token can publish to those sites but not create or
  delete sites). It never has more rights than its user has at that moment,
  and can expire after 30, 90 or 365 days, or never.
- A sign-in ends after 3 days without use and at the latest after 30 days;
  "sign out everywhere" ends all of a person's sign-ins. After 5 failed
  sign-ins for an account (from any address), or 20 sign-ins from one address
  for any names, within 15 minutes, further attempts are refused until the
  window passes, also across restarts.
- Every change is recorded in the activity log, with the token's name when a
  token was used.

## Web interface

The daemon serves the web app on the same address as the API; the app signs
in with name or e-mail address and password. For development run `scripts/dev.sh` and
`npm run dev` in `web/`. For production `make build` builds the app and
embeds it into `bin/ownpaneld` (`make build-linux` for a server). A plain
`go build` leaves the app out and only serves the API.

## API

JSON in and out; errors are `{"error": "Readable sentence"}`. Requests are
authenticated by an access token or by the session cookie. Changing requests
made with the cookie must send `X-Ownpanel-Request: 1`.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/session` | setup needed? setup steps done (`setup_complete`)? who is signed in? can a forgotten password be reset by e-mail (`password_reset`)? |
| `POST` | `/api/setup` | first admin, body `{"setup_code", "name", "password"}` |
| `POST` | `/api/login`, `/api/logout` | sign in (body `{"name", "password"}`; `name` may be the e-mail address) and out |
| `POST` | `/api/password-reset` | `{"email"}`: send a link to choose a new password (always `204`; `404` when the panel cannot send e-mail) |
| `POST` | `/api/password-reset/confirm` | `{"token", "password"}`: set the new password and end all sessions of that person |
| `PUT` | `/api/account/password` | body `{"current", "new"}`; ends your other sessions |
| `PUT` | `/api/account/email` | body `{"email"}` (`""` removes it) |
| `GET` `POST` | `/api/tokens` | your access tokens; create with `{"name", "access": "read"\|"write", "sites": [..] \| null}` |
| `DELETE` | `/api/tokens/{id}` | revoke a token |
| `GET` | `/api/sites` | list sites |
| `GET` `PUT` | `/api/sites/{name}` | show; create or update with `{"domains": ["example.com"]}` |
| `DELETE` | `/api/sites/{name}` | remove site and its files |
| `POST` | `/api/sites/{name}/deployments` | upload a `.zip` or `tar.gz` (max 512 MB); its root becomes the web root (a single top-level folder is unwrapped) |
| `POST` | `/api/uploads/{id}` | upload a `.zip` or `tar.gz` through a one-time upload link (the link is the credential; see MCP) |
| `POST` | `/api/sites/{name}/rollback` | previous release, or body `{"release": "<id>"}` |
| `GET` | `/api/sites/{name}/icon` | the site's icon from its live release (`<link rel="icon">` in `index.html`, else `favicon.svg`/`.png`/`.ico`, `apple-touch-icon.png`); `404` if none |
| `GET` `POST` | `/api/users` | admins: list, create with `{"name", "email", "password", "role"}` (`email` optional) |
| `PUT` `DELETE` | `/api/users/{id}` | admins: change any of `{"role", "email", "password"}`, delete |
| `GET` | `/api/activity?limit=50` | admins: newest activity first |
| `GET` | `/api/server` | `{"ips": [..]}`: the server's addresses (`-server-ip`, else the panel settings) |
| `GET` `PUT` | `/api/settings` | admins: panel domain, server addresses, certificate e-mail (with `*_locked` when set by flag), detected addresses, `setup_complete`, `ip_access`; change with any of `{"panel_domain", "server_ips", "acme_email"}` |
| `GET` | `/api/settings/checks` | admins: does the panel domain point here (`dns`), is a trusted certificate served (`certificate`)? |
| `POST` | `/api/settings/complete` | admins: finish the setup steps |
| `GET` | `/api/updates` | admins (signed in or unrestricted write token): installed version and channel, newest `stable`/`nightly` release, `update_available`, automatic updates, progress of a running update (`status.state`: idle, requested, running, succeeded, failed, rolled_back), `supported`, `checked_at` |
| `POST` | `/api/updates/check` | admins: look for new releases now instead of using the 30-minute cache, at most once a minute (within it, the last result); same answer as `GET /api/updates`, whose `checked_at` tells when the last lookup was and `check_error` why it failed |
| `POST` | `/api/updates` | admins: update with `{"channel": "stable"}` or `{"version": "v1.2.3"}` (202); an older version needs `"allow_downgrade": true` |
| `PUT` | `/api/updates/auto` | admins: automatic updates `{"mode": "off"\|"stable"\|"nightly", "window": "03:00-05:00"}` (installed channel only) |
| `GET` `POST` | `/api/dns/connections` | DNS connections (yours; admins see all); add with `{"provider": "hetzner", "name", "token"}` |
| `POST` | `/api/dns/connections/{id}/check` | list the zones again; the outcome is in `last_error` |
| `DELETE` | `/api/dns/connections/{id}` | remove a connection (admins: anyone's) |
| `GET` | `/api/dns/zones` | `{"zones": [..], "problems": [..]}`: zones of your connections that are in the panel (chosen or in use) |
| `GET` | `/api/dns/zones/available` | the other zones of your connections, to choose from |
| `PUT` `DELETE` | `/api/dns/connections/{id}/zones/{zone}/managed` | manage a zone in the panel, or stop (nothing changes at the provider) |
| `GET` | `/api/dns/connections/{id}/zones/{zone}/records` | record sets of a zone (`ttl` null = zone default) |
| `PUT` `DELETE` | `/api/dns/connections/{id}/zones/{zone}/records/{name}/{type}` | set with `{"ttl": 3600 \| null, "values": [..]}`, or delete; `name` is `@` for the zone itself |
| `GET` | `/api/sites/{name}/domains/status` | per domain: `points_here`, `points_elsewhere`, `no_record` or `lookup_failed`, and the zone "point here" would use |
| `POST` | `/api/sites/{name}/domains/{domain}/point` | write A/AAAA records for the domain; `{"changed": [..]}` |

Tokens and passwords can only be managed when signed in with a password.

The daemon listens on `127.0.0.1:8600`. To reach the panel over HTTPS, set
the panel domain in the setup steps (or start it with `-panel-domain
panel.example.com`) and point that domain at the server: Caddy then serves
the panel on it with an automatic certificate, like any site.

## Publish from GitHub Actions

A workflow that builds the site can publish it with the action in this
repository, using a write token limited to the one site (stored as a secret):

```yaml
- uses: danielgtmn/ownpanel/actions/publish@main
  with:
    panel: https://panel.example.com
    token: ${{ secrets.OWNPANEL_TOKEN }}
    site: blog
    path: dist
```

It packs `path`, uploads it, and sets the outputs `version` and `url`. See
the wiki page [GitHub Action](https://github.com/danielgtmn/ownpanel/wiki/GitHub-Action)
for examples (Vite, Astro, Hugo, Jekyll, plain HTML) and error messages.

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
is then refused by Hetzner, and the connection shows that. Accounts are
connected under **Settings → Connected accounts**.

**Choosing domains.** Connecting an account imports nothing. The panel shows
the zones you choose under **DNS → Add domain** (from your accounts, or
created new at Hetzner), plus every zone that one of your sites, redirects
or mail domains uses (for administrators also the panel's own domain).
**Stop managing here** takes a chosen zone out of the panel without touching
it at Hetzner; deleting it at Hetzner is a separate action. After upgrading, zones in use stay visible and the others
wait under **Add domain**; nothing is deleted.

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
  `dns.record.set`, `dns.record.delete`, `dns.point`, `dns.zone.adopt`,
  `dns.zone.release`); tokens never are, and
  TXT values longer than 60 characters are left out.

**Secrets.** API tokens are stored in the database encrypted (AES-256-GCM)
with a key in `<data>/secret.key`, created on first start (mode 0600). The
API never returns a token after it was added. Without that file the stored
tokens cannot be used; connections then have to be added again.

## Install on a server

On a fresh Debian 12+ or Ubuntu 22.04+ server (amd64 or arm64), as root:

```sh
curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh | sudo bash
```

It downloads the latest release, checks its SHA-256, installs `ownpaneld` and
Caddy as systemd services and prints the address to open
(`https://<server address>/`, with a one-time certificate warning) and the
one-time setup code. Enter the code, create the first admin and follow the
setup steps. Options for unattended installs:

```sh
curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh \
  | sudo bash -s -- --panel-domain panel.example.com --server-ip 203.0.113.5
```

Update from the panel (Settings → Updates: one button, a backup first and an
automatic rollback if the new version does not start, optional automatic
updates in a nightly time window) or on the server with
`sudo /usr/local/lib/ownpanel/update.sh` (`--check` only looks); installations
made before the updater existed get it with
`curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/update.sh | sudo bash`. Running the
installer again upgrades too; `--uninstall` removes ownpanel but
keeps the data (`--purge` deletes it too). `--channel nightly` follows the
build of `main` made after every merged pull request instead of the stable
releases, which come from the `release` branch. Releases are built by the
Publish workflow (`.github/workflows/release.yml`) with `scripts/package.sh`.

Manual install: download `ownpanel_<version>_linux_<arch>.tar.gz` and
`checksums.txt` from the releases, check and unpack the archive and run
`sudo ./install.sh --from-dir .` inside it. `make package` builds the same
archives locally. Flags go into `/etc/ownpanel/ownpaneld.env`
(`OWNPANELD_ARGS`). All options, channels, firewall and DNS:
[Installation](https://github.com/danielgtmn/ownpanel/wiki/Installation).

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
- Sign-in throttling trusts `X-Forwarded-For` from local connections, so
  local users can get around the per-address limit. The limit per account
  lets anyone who knows a name or e-mail address lock that account for 15
  minutes at a time.

## Documentation

The full documentation is in the [wiki](https://github.com/danielgtmn/ownpanel/wiki).
It is generated from the `wiki/` folder of this repository; edit it there.

## License

ownpanel is licensed under the [GNU Affero General Public License v3.0](LICENSE).
