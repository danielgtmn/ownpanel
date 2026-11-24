# Configuration

ownpaneld has two commands:

```
ownpaneld [serve] [flags]                    run the daemon
ownpaneld backup -data <dir> -out <file>     write a backup archive
```

`serve` is the default: if the first argument starts with `-` (or there is none), the daemon runs. Any other unknown command is an error ("use serve or backup"). Flags use Go's syntax: `-name value` or `-name=value`.

On a server installed with `deploy/install.sh`, flags live in `/etc/ownpanel/ownpaneld.env` (see [[Installation|Installation]]).

## `ownpaneld serve`

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `127.0.0.1:8600` | Address of the panel: web interface, API and MCP, plain HTTP. |
| `-data` | `./data` | Directory for the database, site files and certificates. Made absolute at start. |
| `-caddy-admin` | `http://127.0.0.1:2019` | Caddy's admin API: `http://host:port` or `unix:///absolute/path.sock`. The socket path must be absolute. The installer and `scripts/dev.sh` use a Unix socket. |
| `-tls` | `acme` | Certificates: `acme` (public certificates), `internal` (Caddy's own CA, self-signed, for testing) or `off` (plain HTTP only, no automatic HTTPS). |
| `-http-port` | `80` | Port for HTTP. With `-tls off` the sites are served on this port. |
| `-https-port` | `443` | Port for HTTPS. |
| `-acme-email` | empty | Contact address given to the certificate authority. Optional; only used with `-tls acme`. |
| `-panel-domain` | empty | Serve the panel itself through Caddy on this domain, with a certificate like any site. The domain cannot also be used by a site. Also makes `/mcp` reachable on that domain and builds upload links on it. |
| `-reconcile-interval` | `30s` | How often the web server is re-synced in the background (a Go duration such as `30s` or `1m`). |
| `-server-ip` | none | Public IPv4/IPv6 address of this server that domains should point at. Repeat the flag or separate values with commas. Must be a public address unless `-tls` is `internal` or `off`. Without it the domain status and "point to this server" are unavailable. |

`-listen` with a wildcard host (`:8600`, `0.0.0.0:8600`) is reached by Caddy through `127.0.0.1`. The daemon does not terminate TLS itself; use `-panel-domain` for HTTPS (see [[Security|Security]]).

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

See [[Backup and Restore|Backup-and-Restore]].

## Version

The daemon reports a version to MCP clients. It is `dev` unless a build sets it with `-ldflags "-X main.version=…"`.

## Data directory layout

Everything the daemon owns is inside the `-data` directory:

```
<data>/
  ownpanel.db            SQLite database (WAL mode: also ownpanel.db-wal, ownpanel.db-shm while running)
  secret.key             32-byte key that encrypts stored DNS tokens (mode 0600)
  setup-code             one-time first-run code; exists only while no user exists
  sites/
    <site>/
      releases/<id>/     one directory per version; never modified after creation
      current            symlink to the live version
  caddy/                 Caddy's storage: certificates and keys
  state.json.imported    only if sites were imported from an old state.json
```

- **`ownpanel.db`** holds users (password hashes), sessions, access tokens (hashed), sites and their domains, DNS connections (token encrypted), the activity log and settings. It is created with mode 0600. The schema is migrated automatically at start.
- **`secret.key`** is created on first start. Without it the stored DNS tokens cannot be used; connections must be added again. It is included in backups.
- **`setup-code`** is created when no user exists and deleted once the first admin is created (or at start, if a user already exists).
- **`sites/<site>/releases/<id>`**: an id looks like `20260131T120000.000Z-ab12cd`. The newest five versions of a site are kept; the live one is never removed.
- **`caddy/`** is where the generated Caddy config tells Caddy to keep certificates and keys.
- **`state.json`**: an old JSON file with sites. If it exists and the database has no sites, they are imported once and the file is renamed to `state.json.imported`.

Not inside `-data` on a server install: `/var/lib/ownpanel/caddy-home` (Caddy's home with its saved config) and `/etc/ownpanel` (units' environment file and the Caddy bootstrap file). With `scripts/dev.sh`, `./data` also holds `caddy-home/`, `caddy-admin.sock` and `caddy-bootstrap.json`.
