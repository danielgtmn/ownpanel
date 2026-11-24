# Development

## Prerequisites

- **Go**: the version in `go.mod` (currently `go 1.26.0`).
- **Node.js and npm** for the web app. The Makefile loads nvm if `~/.nvm/nvm.sh` exists and then runs `npm ci` (only if `web/node_modules` is missing) and `npm run build`. The project does not pin a Node version; use a current release.
- **Caddy** for running the whole thing locally: `scripts/dev.sh` installs it into `bin/` with `go install` if it is missing.

## Make targets

| Target | What it does |
|---|---|
| `make web` | Builds the web app into `internal/webui/dist` (installs dependencies if needed) |
| `make build` | `make web`, then `go build -tags webui -o bin/ownpaneld ./cmd/ownpaneld`: a binary with the web interface embedded |
| `make build-linux` | `make web`, then the same for Linux, amd64, `CGO_ENABLED=0`: `bin/linux-amd64/ownpaneld` |
| `make test` | `go vet ./... && go test ./...` |
| `make dev` | `scripts/dev.sh`: Caddy and ownpaneld locally on ports 8080/8443 with self-signed certificates |

A plain `go build ./cmd/ownpaneld` works without the web build and serves only the API (`/` answers with a short note). `internal/webui/dist` is not committed.

## Running locally

See [[Getting Started|Getting-Started]]. In short: `scripts/dev.sh` in one terminal, `npm run dev` in `web/` in another (port 5173, forwards `/api` to `127.0.0.1:8600`; `/mcp` is not forwarded).

## Tests

```sh
go test ./...                  # Go unit and integration tests
go vet ./...
cd web && npm test             # web app tests (vitest, watch mode)
cd web && npm test -- --run    # once
cd web && npm run lint         # oxlint
cd web && npm run build        # type check, then bundle
INSECURE=1 scripts/smoke.sh    # end-to-end against a running scripts/dev.sh
```

`scripts/smoke.sh` signs in (or does the first-run setup), creates a site, deploys, redeploys, rolls back, checks that a malicious archive and a cookie request without the header are refused, then deletes the site. Its options (`API`, `DOMAIN`, `WEB`, `HTTP_PORT`, `HTTPS_PORT`, `SITE`, `CERT_WAIT`, `SETUP_CODE_FILE`, `ADMIN_NAME`, `ADMIN_PASSWORD`, `OWNPANEL_TOKEN`, `KEEP`, `INSECURE`) are documented at the top of the script.

### Live tests

Two tests talk to real services and are behind the `live` build tag, so `go test ./...` never runs them:

| Test | Command | Needs |
|---|---|---|
| Hetzner Cloud API: lists zones, creates and removes records named `_ownpanel-live-test*` in the given zone, leaves nothing else changed | `HETZNER_TOKEN=… HETZNER_TEST_ZONE=example.com go test -tags live -run Live -v ./internal/dns/hetzner` | `HETZNER_TOKEN`: a Read & Write token; `HETZNER_TEST_ZONE`: a primary zone in that project. Skipped if either is unset |
| Authoritative DNS lookups: read-only, logs what a few names resolve to | `go test -tags live -run Live -v ./internal/dns` | network access; no environment variables (the names it looks up are written in the test file) |

## Conventions

- **Permission checks only in `internal/panel`.** The API (`internal/api`), MCP (`internal/mcp`) and any future front door only authenticate and translate. If you need a new rule, add it to `internal/panel` so every front door gets it.
- **New API route groups** are a new file plus an entry in `routeGroups` (`internal/api/api.go`).
- **UI text** (from `web/AGENTS.md`): text shown to people describes what happens for them. No library, protocol or implementation names in labels, titles, hints or messages. Passwords and token secrets are never written to storage or logs. Example: say "Simplify names", not the name of the algorithm behind it.
- **Web modules** never import from each other; see [[Architecture|Architecture]].
- **The web app** is built with ui-sdk. Before using it, read `web/AGENTS.md` and the `ui-sdk` skill it points to.
- **Error messages** from `internal/panel` are meant to be shown to people as they are; internal errors are logged, never sent to clients.
- **Database changes** are new numbered files in `internal/store/migrations/`; they are applied at start.
- Release builds can set the reported version: `-ldflags "-X main.version=…"`.
