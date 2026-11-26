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
| `make package VERSION=v1.2.3` | `scripts/package.sh`: release archives for linux/amd64 and linux/arm64 plus `checksums.txt` in `dist/`, exactly as the release workflow builds them (Caddy is downloaded from its official release and checked against a pinned SHA-512) |
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
scripts/commit-type_test.sh    # the commit message check
actions/publish/publish_test.sh  # the GitHub Action against a real ownpaneld
```

`actions/publish/publish_test.sh` builds `ownpaneld` (or uses `OWNPANELD=<path>`), starts it with a stand-in for Caddy's admin API (needs `python3`), creates sites and tokens, and runs the action's script for success and every error it explains (wrong token, unknown site, read-only token, missing or empty folder, too large, unreachable panel). CI runs it in the `action` job when `actions/` or the API changes.

`scripts/smoke.sh` signs in (or does the first-run setup), creates a site, deploys, redeploys, rolls back, checks that a malicious archive and a cookie request without the header are refused, then deletes the site. Its options (`API`, `DOMAIN`, `WEB`, `HTTP_PORT`, `HTTPS_PORT`, `SITE`, `CERT_WAIT`, `SETUP_CODE_FILE`, `ADMIN_NAME`, `ADMIN_PASSWORD`, `OWNPANEL_TOKEN`, `KEEP`, `INSECURE`) are documented at the top of the script.

### Live tests

These tests talk to real services and are behind build tags, so `go test ./...` never runs them:

| Test | Command | Needs |
|---|---|---|
| Hetzner Cloud API: lists zones, creates and removes records named `_ownpanel-live-test*` in the given zone, leaves nothing else changed | `HETZNER_TOKEN=… HETZNER_TEST_ZONE=example.com go test -tags live -run Live -v ./internal/dns/hetzner` | `HETZNER_TOKEN`: a Read & Write token; `HETZNER_TEST_ZONE`: a primary zone in that project. Skipped if either is unset |
| Authoritative DNS lookups: read-only, logs what a few names resolve to | `go test -tags live -run Live -v ./internal/dns` | network access; no environment variables (the names it looks up are written in the test file) |
| Build isolation: hostile builds against the real build service (reading the secret key, reaching the panel and private addresses, fork bomb, filling the disk, links in the output, ignoring the time limit, leaking secrets, a restart in the middle, the packet filter going away) | `GOOS=linux go test -c -tags podman -o isolation.test ./internal/build`, then on the server `sudo OWNPANEL_PANEL_ADDR=127.0.0.1:8600 ./isolation.test -test.v` | a server (or a systemd container with loop devices) where `install.sh` set builds up; runs as root, briefly removes and restores the packet filter and restarts the build service |

## Conventions

- **Permission checks only in `internal/panel`.** The API (`internal/api`), MCP (`internal/mcp`) and any future front door only authenticate and translate. If you need a new rule, add it to `internal/panel` so every front door gets it.
- **New API route groups** are a new file plus an entry in `routeGroups` (`internal/api/api.go`).
- **UI text** (from `web/AGENTS.md`): text shown to people describes what happens for them. No library, protocol or implementation names in labels, titles, hints or messages. Passwords and token secrets are never written to storage or logs. Example: say "Simplify names", not the name of the algorithm behind it.
- **Web modules** never import from each other; see [[Architecture|Architecture]].
- **The web app** is built with ui-sdk. Before using it, read `web/AGENTS.md` and the `ui-sdk` skill it points to.
- **Error messages** from `internal/panel` are meant to be shown to people as they are; internal errors are logged, never sent to clients.
- **Database changes** are changes to the row structs in `internal/store/repos.go` ([ablage](https://github.com/danielgtmn/ablage) repositories): ablage adds new tables, fields and indexes at start, and never drops or renames anything, so rename a field only with a `db` tag that keeps the old column name. No joins: load related rows in one query per relation. The SQL files in `internal/store/legacy/` are the schema from before ablage; they are only used to convert old databases once (`internal/store/legacy.go`) and never change.
- Release builds can set the reported version: `-ldflags "-X main.version=…"`.
- **Commit messages and pull request titles** follow [Conventional Commits](https://www.conventionalcommits.org/): `<type>(<scope>): <description>`, scope optional, types `feat`, `fix`, `perf`, `docs`, `chore`, `ci`, `refactor`, `test`, `build`, e.g. `feat(dns): add Hetzner zones`. The version of a stable release is computed from them (see [Releases](#releases)). Pull requests are merged with a merge commit, so the version comes from the individual commits, not from the title: write every commit that lands on `main` in this format. The required check `commits` checks the title and every commit of a pull request into `main`; reword commits that fail with `git rebase -i`. Pull requests into `release` only warn about older commits and show which release the merge will make.

## Releases

Two channels, both built by `.github/workflows/release.yml` with `scripts/package.sh` (see [[Installation|Installation#release-channels]]):

- **Nightly:** every push to `main` that changes something built into the archives (`cmd/`, `internal/`, `web/`, `go.mod`/`go.sum`, `install.sh`, `update.sh`, `deploy/`, `scripts/package.sh`, `Makefile`) rebuilds the prerelease `nightly`; pushes that only change documentation, the wiki or other workflows build nothing: the tag `nightly` moves to the new commit, the archives are replaced, the version is `nightly-YYYYMMDD-<commit>`. Never marked "latest".
- **Stable:** releases are made from the long-lived branch `release`. To release, first bring [[Roadmap|Roadmap]] (`wiki/Roadmap.md`) up to date in a pull request into `main` (feature pull requests leave it alone, so they do not all change the same lines), then open a pull request `main → release` and merge it with a merge commit. The push to `release` starts the workflow: its `tag` job reads the commits since the last `v*` tag and computes the next version, `fix:`/`perf:` the patch, `feat:` the minor and `feat!:` (or a `BREAKING CHANGE:` line) the major version, and pushes the tag `v<version>`. The `release` job then builds and publishes it as the "latest" release, with notes listing the pull requests merged since the previous tag. A push without `feat:`, `fix:` or `perf:` commits releases nothing. The `commits` check on that pull request shows which release it will make.

Pull requests and pushes to `release` run CI like `main`. CI only runs the jobs whose files changed: on pull requests compared with the base branch, on pushes compared with the previous commit. If a stable release fails after its tag was pushed, use "Re-run failed jobs" on the run (re-running the `tag` job finds the tag and releases nothing). The workflow can also be started by hand: `dry-run` builds the archives without publishing (kept as a workflow artifact for 7 days), `nightly` publishes a nightly from `main` now.
