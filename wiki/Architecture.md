# Architecture

## Components

| Component | Role |
|---|---|
| **Caddy** | Serves the sites and obtains certificates. Runs as its own process. Its admin API is a Unix socket that only ownpaneld can reach. |
| **ownpaneld** | The panel daemon (Go). Keeps the panel's data in SQLite, builds a complete Caddy config from it and loads it through Caddy's admin API. Serves the API, MCP and the web interface. |
| **SQLite** | `ownpanel.db` in the data directory: users, sessions, access tokens, sites and domains, DNS connections, activity log. Sign-in accounts, password hashes, sessions, attempt counts and reset links live in the `auth_*` tables of the same file, managed by the [sesam](https://github.com/danielgtmn/sesam) library on the same [ablage](https://github.com/danielgtmn/ablage) store as the panel's own tables; `users.auth_user_id` references the sign-in account (`auth_users`) of a panel user (id and role), so a sign-in account cannot be removed while a panel user points at it. Stored secrets (DNS tokens, DKIM keys) are encrypted with the [panzerschrank](https://github.com/danielgtmn/panzerschrank) library and the key in `secret.key` next to the database. |
| **ownpanel-mail** | The mail server (`ownpaneld mail`), its own service and user. Receives, stores and sends mail; ownpaneld pushes its full configuration over a local socket, like Caddy's. See [[E-Mail architecture|E-Mail-Architecture]]. |
| **Web app** | React app built with Vite into `internal/webui/dist`. Embedded into the binary with the `webui` build tag (`make build`, `make build-linux`). A plain `go build` leaves it out and serves only the API. |

Site files are not served by ownpaneld: Caddy reads them from the data directory.

## Desired state and the reconcile loop

The database is the **desired state**: which sites exist, with which domains. The files of each site are immutable versions on disk; a symlink `current` points to the live one.

`Sync` turns this into one **complete Caddy config** and loads it with `POST /load` on the admin API. The whole config is rebuilt on every change instead of patching routes. Caddy applies a load gracefully (no dropped connections) and ignores it when nothing changed, so syncing is safe to repeat.

The config contains:

- if `-panel-domain` is set, a route that proxies that host to the daemon's own address (so the panel gets a certificate like any site),
- one route per site that has domains: its hosts are served from the site's `current` directory with `file_server` (zstd and gzip), or answer `503` until the first version exists,
- a catch-all that answers `404` "Unknown site.",
- TLS settings for the chosen mode (`acme`, `internal`, `off`) and the storage directory `<data>/caddy`.

Sync runs

- right after every change that affects Caddy (creating a site, changing domains, deleting a site, the first publish of a site; later publishes and switching versions are only a symlink change, no Caddy update needed),
- once at start, and then every `-reconcile-interval` (default 30 seconds) in the background, so drift heals by itself, for example when Caddy was restarted,
- every 2 seconds while it is failing, until it works again.

If a change is saved but the sync fails, the API answers `502` with "saved, but the web server could not be updated yet".

## How a request flows

**A visitor** requests `https://example.com/`: Caddy matches the host to the site's route and serves the file from `<data>/sites/<site>/current`. ownpaneld is not involved.

**A person or program** calls the panel:

1. Caddy receives the request on the panel domain and proxies it to ownpaneld (adding `X-Forwarded-For` and `X-Forwarded-Proto`, which ownpaneld trusts only from a loopback peer).
2. ownpaneld routes by path: `/api/` to the HTTP API, `/mcp` to the MCP server, everything else to the web app (the app itself asks `/api/session` whether someone is signed in).
3. The front door **authenticates** the request: the API accepts a bearer token or the session cookie; MCP accepts bearer tokens only. The result is an `Actor`: who, their role, read-only or site-limited.
4. The front door decodes the request and calls `internal/panel`, which **decides permissions** and performs the operation: store, deploy, DNS, sync.
5. Errors from `internal/panel` carry a kind (not found, invalid, conflict, forbidden, ...) that the front doors map to HTTP status codes or MCP tool errors.

## Package map

### Go (`internal/` and `cmd/`)

| Package | Responsibility |
|---|---|
| `cmd/ownpaneld` | Entry point: the `serve` and `backup` commands, flags, wiring, routing of `/api/`, `/mcp` and the web app |
| `internal/panel` | All operations and **all permission checks**; actors, tokens, sign-in (through sesam's Go API), password reset by e-mail, upload links, DNS rules. Every front door calls this |
| `internal/store` | the panel's data in [ablage](https://github.com/danielgtmn/ablage) repositories on SQLite (`repos.go`), the one-time conversion of databases from before (`legacy.go`), validation of names and domains |
| `internal/caddy` | Caddy config builder and admin API client |
| `internal/deploy` | Versions ("releases"): unpacking archives, activation, going back, pruning |
| `internal/github` | Small GitHub client: creating an App from a manifest, signing in as the App (JWT), installations, short-lived tokens, repositories; `githubtest` is a stand-in for tests and development |
| `internal/dns` | Provider interface, zone lookup, "does this domain point here?", lookups at authoritative nameservers; `dns/hetzner` is the Hetzner Cloud client |
| `internal/api` | HTTP API: authentication, then a thin adapter over `internal/panel` |
| `internal/mcp` | MCP server at `/mcp`: the same operations as tools |
| `internal/backup` | Backup archives |
| `internal/webui` | Serves the web app (embedded with `-tags webui`) |

New API routes: add a file with a method that registers them and list it in `routeGroups` in `internal/api/api.go`.

### Web (`web/src`)

| Folder | Responsibility |
|---|---|
| `app/` | The shell: header with account menu, navigation, hash routing, first-run setup and sign-in screens, global styles |
| `shared/` | Code every module may use: `api/` (requests, errors, shared lookups), `session/` (who is signed in, `useSession()`, name and password rules), `format/` (dates), `routing/`, `hooks/`, `ui/` (small UI pieces) |
| `modules/<name>/` | One area of the panel: `sites`, `dns`, `access` (access tokens), and the admin-only `users` (People) and `activity` |
| `modules/registry.ts` | The list of modules; navigation and routes come only from this list |

## Adding a module to the web app

1. Create `web/src/modules/<name>/` with an `index.ts` that exports a `PanelModule`: `id`, `label`, `basePath`, `page` (loads the page component on demand, `() => import('./MyPage.tsx').then((page) => page.MyPage)`; the component receives the remaining path `segments`) and optionally `visible: (user) => boolean` to hide it (and its address) from some people.
2. Add one line to `web/src/modules/registry.ts`.

Only a module's page is loaded when it is first opened; the rest of its `index.ts` (label, sidebar rows, counts, `visible`) is part of the first download, so keep it light and don't import page components there.

Modules never import from each other; shared code goes into `shared/`. Text shown to people follows the rules in [[Development|Development]].

A new backend capability starts in `internal/panel` (operation and permissions), then gets a thin API route and, if useful, an MCP tool.
