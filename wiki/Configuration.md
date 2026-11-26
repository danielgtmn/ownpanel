# Configuration

ownpaneld has these commands:

```
ownpaneld [serve] [flags]                    run the daemon
ownpaneld backup -data <dir> -out <file>     write a backup archive
ownpaneld mail [flags]                       run the mail service
ownpaneld build-worker [flags]               run the build service
ownpaneld version                            print the version
```

`serve` is the default: if the first argument starts with `-` (or there is none), the daemon runs. Any other unknown command is an error. Flags use Go's syntax: `-name value` or `-name=value`.

On a server installed with `install.sh`, flags live in `/etc/ownpanel/ownpaneld.env` (see [[Installation|Installation]]).

## `ownpaneld serve`

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:8600` | Address of the panel: web interface, API and MCP, plain HTTP. |
| `-data` | `./data` | Directory for the database, site files and certificates. Made absolute at start. |
| `-caddy-storage` | `caddy` in `-data` | Where the generated Caddy config tells Caddy to keep certificates and keys. The server install uses `/var/lib/ownpanel-caddy/storage` (set in `ownpaneld.service`). |
| `-update-dir` | `update` in `-data` | Where the panel leaves update requests for the root-side updater and reads its status. The server install uses `/var/lib/ownpanel-update/requests` (set in `ownpaneld.service`). |
| `-caddy-admin` | `http://127.0.0.1:2019` | Caddy's admin API: `http://host:port` or `unix:///absolute/path.sock`. The socket path must be absolute. The installer and `scripts/dev.sh` use a Unix socket. |
| `-tls` | `acme` | Certificates: `acme` (public certificates), `internal` (Caddy's own CA, self-signed, for testing) or `off` (plain HTTP only, no automatic HTTPS). |
| `-http-port` | `80` | Port for HTTP. With `-tls off` the sites are served on this port. |
| `-https-port` | `443` | Port for HTTPS. |
| `-acme-email` | empty | Contact address given to the certificate authority. Optional; only used with `-tls acme`. Overrides the panel settings (see below). |
| `-panel-domain` | empty | Serve the panel itself through Caddy on this domain, with a certificate like any site. The domain cannot also be used by a site. Also makes `/mcp` reachable on that domain and builds upload links on it. Overrides the panel settings (see below). |
| `-mail-admin` | empty | Admin socket of the mail service (`ownpaneld mail`) as `unix:///absolute/path.sock`; the installer passes `unix:///run/ownpanel-mail/admin.sock`. Empty: no mail service; mail domains and mailboxes are only stored. See [[E-Mail architecture|E-Mail-Architecture]]. |
| `-mail-web` | empty | Web socket of the mail service (`ownpaneld mail -web`) as `unix:///absolute/path.sock`; the installer passes `unix:///run/ownpanel-mail-web/web.sock`. With it, and a mail server name different from the panel domain, Caddy serves the mail server name over HTTPS for mailbox users (their sign-in page). Empty: no such site. See [[Groupware architecture|Groupware-Architecture]]. |
| `-build-worker` | empty | Socket of the build service (`ownpaneld build-worker`) as `unix:///absolute/path.sock`; the installer passes `unix:///run/ownpanel-build/worker.sock`. The panel asks it whether this server can build sites. Empty: builds are reported as not set up. |
| `-mail-host` | empty | Name of the mail server (MX target, what mail programs connect to), e.g. `mail.example.com`. It cannot be a site, redirect or mail domain; it may be the panel domain. Overrides the panel settings (see below). |
| `-reconcile-interval` | `30s` | How often the web server is re-synced in the background (a Go duration such as `30s` or `1m`). |
| `-releases-api` | `https://api.github.com` | Where the panel looks for new releases ([[Installation|Installation#updating]]). Only change it for testing. |
| `-server-ip` | none | Public IPv4/IPv6 address of this server that domains should point at. Repeat the flag or separate values with commas. Must be a public address unless `-tls` is `internal` or `off`. Overrides the panel settings (see below). Without addresses (here or in the settings) the domain status and "point to this server" are unavailable. |

`-listen` with a wildcard host (`:8600`, `0.0.0.0:8600`) is reached by Caddy through `127.0.0.1`. The daemon does not terminate TLS itself; Caddy serves the panel over HTTPS on the panel domain and, during setup, on the server's addresses (see [[Security|Security]]).

## Panel settings

The panel domain, the server's public addresses and the certificate e-mail are normally entered in the panel: in the first-run wizard right after the first admin is created, and later by admins under the panel settings (`GET`/`PUT /api/settings`). They are stored in the database (`settings` table) and applied at once, without a restart.

The mail server's name (`mail_host`) and the postmaster address (receives `postmaster@` and `abuse@` of the mail server and of domains without mailboxes) are panel settings too, changed by admins only. The certificate for the mail server is kept in `<data>/mail-tls`: with `-tls acme` from the public CA (the challenge comes in through Caddy on port 80, once the name points at this server), otherwise self-signed.

The **domain for previews** (`preview_domain`) is a panel setting too, for admins only (Settings → Previews). Every kept version of a site gets an address `<label>.<preview domain>` ([[Sites and Publishing|Sites-and-Publishing#previews]]). Empty (the default) means versions have no preview address. It needs a wildcard DNS record `*.<preview domain>` (A and AAAA) to the server's addresses; when the domain is in a zone of a DNS account connected in the panel, Settings offers to create it ("Point to this server", `POST /api/settings/preview-domain/point`), otherwise it shows the records to create, and it shows whether the names point here. The panel suggests `preview.<panel domain>`, but a domain of its own (not below the panel's) is the safer choice ([[Security|Security#previews]]). It must be a plain host name with a dot; the panel domain, the mail server's name, and the domains of sites and redirects may not be below it. There is no flag for it.

The flags `-panel-domain`, `-server-ip`, `-acme-email` and `-mail-host` still work and **win** over the stored values. A value set by flag is shown as locked ("set in the server configuration") and cannot be changed in the panel; remove the flag and restart to manage it in the panel again. Installs that pass these flags behave as before.

Rules for the settings: the panel domain is a plain host name with at least one dot, not an IP address and not a domain of any site. Server addresses follow the rules of `-server-ip`. The e-mail must be a plain address (`you@example.com`). The panel suggests the machine's own addresses (it reads its network interfaces; no outside service is asked).

**Access by IP address.** Caddy also serves the panel on `https://<server address>/` (all configured addresses, the detected public ones, and `127.0.0.1`/`::1`) with a certificate from Caddy's internal CA, so a new install can be opened before it has a domain (the browser warns once). Plain HTTP on those addresses redirects to HTTPS. Once the wizard is **completed with a panel domain set**, those addresses redirect to `https://<panel domain>/` instead. If the wizard is completed without a domain, the panel stays reachable by address. Databases from before the wizard count as set up already.

Examples:

```sh
ownpaneld -server-ip 203.0.113.5 -server-ip 2001:db8::5
ownpaneld -server-ip 203.0.113.5,2001:db8::5
ownpaneld -data ./data -tls internal -http-port 8080 -https-port 8443 \
  -caddy-admin unix:///abs/path/data/caddy-admin.sock
```

There is no configuration file and there are no other environment variables for the daemon; the install uses `OWNPANELD_ARGS` only as a way for systemd to pass the flags above.

## `ownpaneld backup`

| Flag | Default | Meaning |
|---|---|---|
| `-data` | `./data` | Data directory of the daemon. |
| `-out` | none (required) | Archive to write, `.tar.gz`. The file must not exist; it is created with mode 0600. |
| `-mail-data` | `/var/lib/ownpanel-mail` | Data directory of the mail service; skipped if it does not exist. |
| `-caddy-storage` | `/var/lib/ownpanel-caddy/storage` | Caddy's certificates; if it does not exist, `caddy/` in the data directory. |
| `-no-mail` | off | Leave the stored mail out of the archive. |

See [[Backup and Restore|Backup-and-Restore]].

## `ownpaneld mail`

The mail service ([[E-Mail|E-Mail]]). `install.sh` runs it as `ownpanel-mail.service`; everything else (domains, mailboxes, certificate, limits) comes from the panel over the admin socket.

| Flag | Default | Meaning |
|---|---|---|
| `-data` | `/var/lib/ownpanel-mail` | Its data: last configuration, mail index, messages, queue. |
| `-admin` | `/run/ownpanel-mail/admin.sock` | Unix socket the panel configures it through (`-mail-admin unix://…` of `serve`). |
| `-web` | empty | Unix socket for the web requests of the mail server name that Caddy passes on (account pages; later calendars and contacts), as `unix:///absolute/path.sock`; the installer passes `unix:///run/ownpanel-mail-web/web.sock` (`-mail-web` of `serve`). Empty: closed. It trusts `X-Forwarded-For` from Caddy, so only Caddy may be able to connect (see [[Installation|Installation]]). |
| `-web-group` | empty | Group the web socket and its directory are handed to (the installer's unit: `ownpanel-mail-web`, of which only Caddy's user is a member). The service must be a member itself. Empty: they keep the service's group. |
| `-smtp` | `:25` | Incoming mail from other servers. |
| `-submission` | `:587` | Mail programs sending mail (STARTTLS before sign-in); `""` turns it off. |
| `-submissions` | `:465` | Mail programs sending mail over TLS; `""` turns it off. |
| `-imaps` | `:993` | Mail programs reading mail (IMAP over TLS); `""` turns it off. |
| `-managesieve` | `:4190` | Mail programs editing a mailbox's filters (ManageSieve, STARTTLS before sign-in); `""` turns it off. |
| `-ipv6-outbound` | off | Also deliver outgoing mail over IPv6. Only turn it on when the server's IPv6 address has the mail server name as reverse name. |
| `-allow-private-mx` | off | **For test setups only.** Also deliver outgoing mail to mail servers whose addresses are loopback, private (e.g. `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`), link-local or otherwise not public. Off, mail to a domain whose mail servers have only such addresses is returned to the sender, so nobody can make the server connect into its own network. |

## Builds

The build service ([[Security|Security#builds]], [[Installation|Installation#building-sites]]). `install.sh` writes its settings to `/etc/ownpanel/build.env` once and keeps them on upgrades. Sizes are numbers with `K`, `M`, `G` or `T` (powers of 1024). Apply a change with `systemctl restart ownpanel-build-setup ownpanel-build`.

| Setting | Default | Meaning |
|---|---|---|
| `OWNPANEL_BUILDS` | `on` | `off` after `install.sh --no-builds`; the installer then leaves builds off. |
| `OWNPANEL_BUILD_DISK_SIZE` | 15 % of the disk, 8G to 32G | Size of the build disk, reserved in full on the server's disk. Changing it makes a new, empty build disk (caches and images are fetched again). It must hold all slots, the images, sources, results and one cache. |
| `OWNPANEL_BUILD_SLOTS` | `1`, or `2` with 6 GB of memory or more | How many builds run at once (1 to 16). Builds of one site never run at the same time. |
| `OWNPANEL_BUILD_WORKSPACE_SIZE` | `4G` (`2G` on small disks) | Disk space of one build: its source tree and everything it writes. |
| `OWNPANEL_BUILD_CACHE_SIZE` | `1G` | Disk space of one site's build cache (`/cache` in a build, where its home and the package managers' downloads are). |
| `OWNPANEL_BUILD_CACHES` | `100` | How many sites keep a cache. When all are taken, or the build disk runs low, the cache used longest ago is deleted. |
| `OWNPANEL_BUILD_IMAGES_SIZE` | `3G` | Disk space for the build images. |
| `OWNPANEL_BUILD_SOURCES_SIZE` | `1G` | Disk space for the source trees of builds that wait for a slot. |
| `OWNPANEL_BUILD_RESULTS_SIZE` | `1G` | Disk space for logs and outputs of finished builds until the panel fetched them. |

The limits of one build (processor, memory, processes, time, log) are chosen per build by the panel, up to fixed maxima the service lowers to the server: 4 CPUs (at most the server's), 4 GB of memory (at most half the server's), 2048 processes, 1 hour, 4 MB of log. Without a choice a build gets 2 CPUs, 2 GB, 1024 processes, 15 minutes and 2 MB.

### `ownpaneld build-worker`

Run by `ownpanel-build.service` as `ownpanel-build`; it refuses to run as root. Its flags come from `build.env`:

| Flag | Default | Meaning |
|---|---|---|
| `-socket` | `/run/ownpanel-build/worker.sock` | Unix socket the panel uses (`-build-worker unix://…` of `serve`). |
| `-work` | `/var/lib/ownpanel-build/work` | Mount point of the build disk. |
| `-resolvers` | `/run/ownpanel-build-setup/resolvers` | Name servers for builds, written by `ownpanel-build-setup`. |
| `-run-dir` | `/run/ownpanel-build/podman` | Podman's private runtime directory. |
| `-slots`, `-workspace-size`, `-cache-size`, `-caches` | | As in `build.env`; they must match the quotas `ownpanel-build-setup` set, or builds are reported unavailable. |

## Version

`ownpaneld version` prints the version, which is also reported to MCP clients. It is `dev` unless a build sets it with `-ldflags "-X main.version=…"`; release builds (`scripts/package.sh`) set the release name, e.g. `v1.2.3` or `nightly-20260131-ab12cd3`.

## Data directory layout

Everything the daemon owns is inside the `-data` directory:

```
<data>/
  ownpanel.db            SQLite database (WAL mode: also ownpanel.db-wal, ownpanel.db-shm while running)
  ownpanel.db.pre-ablage-<time>  copy of the database from before its one-time conversion (only after that update)
  ownpanel.db.pre-auth-ref-<time>  copy from before a one-time fix of the users table (only for a database converted by a nightly build before the fix)
  secret.key             32-byte key that encrypts stored DNS tokens (mode 0600)
  setup-code             one-time first-run code; exists only while no user exists
  sites/
    <site>/
      releases/<id>/     one directory per version; never modified after creation
      current            symlink to the live version
      pending/<id>       empty marker: version published without being made live, and not live since
  caddy/                 Caddy's storage: certificates and keys
  state.json.imported    only if sites were imported from an old state.json
```

- **`ownpanel.db`** holds users (password hashes), sessions, access tokens (hashed), sites and their domains, DNS connections (token encrypted), the activity log and settings (among them the secret preview addresses are derived from, encrypted with `secret.key`). It is created with mode 0600. The schema is migrated automatically at start. A database from before the panel kept its data with [ablage](https://github.com/danielgtmn/ablage) is converted once at start; the copy from before is kept as `ownpanel.db.pre-ablage-<UTC time>` (mode 0600) and may be deleted once the new version runs well (see [[Installation|Installation#updating]]). A database converted by a nightly build before panel users referenced their sign-in account gets that reference once at start, after a copy `ownpanel.db.pre-auth-ref-<UTC time>` (mode 0600); it may be deleted the same way.
- **`secret.key`** is created on first start. Without it the stored DNS tokens cannot be used; connections must be added again. It is included in backups.
- **`setup-code`** is created when no user exists and deleted once the first admin is created (or at start, if a user already exists).
- **`sites/<site>/releases/<id>`**: an id looks like `20260131T120000.000Z-ab12cd`. The newest five versions of a site are kept, and besides them the live one and the one going back would return to ([[Sites and Publishing|Sites-and-Publishing#versions]]).
- **`caddy/`** is where the generated Caddy config tells Caddy to keep certificates and keys, unless `-caddy-storage` names another directory (a server install uses `/var/lib/ownpanel-caddy/storage`).
- **`state.json`**: an old JSON file with sites. If it exists and the database has no sites, they are imported once and the file is renamed to `state.json.imported`.

Not inside `-data` on a server install: `/var/lib/ownpanel-caddy` (Caddy's certificates in `storage`, its home with the saved config in `home`), `/var/lib/ownpanel-update` (update requests and status in `requests`, backups made before updates in `backups`) and `/etc/ownpanel` (units' environment file and the Caddy bootstrap file). With `scripts/dev.sh`, `./data` also holds `caddy-home/`, `caddy-admin.sock` and `caddy-bootstrap.json`.
