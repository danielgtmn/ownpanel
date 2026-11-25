# AGENTS.md

Guidance for AI coding agents (and people) working on ownpanel. The web
interface has its own rules in [`web/AGENTS.md`](web/AGENTS.md); read both
when you touch `web/`. User-facing documentation lives in [`wiki/`](wiki/)
and is copied to the GitHub wiki by a workflow: edit it there.

## What this is

ownpanel is a self-hosted hosting panel: `ownpaneld` (Go) keeps the desired
state in SQLite and drives Caddy through its admin socket; a React app
(embedded into the binary with the `webui` build tag) is the interface; an
MCP server exposes the same operations to AI agents. Static sites, DNS via
the Hetzner Cloud API, redirects, users and access tokens exist today;
e-mail and apps are planned (see the issues).

## Layout

```
cmd/ownpaneld/        entry point: flags, wiring, serve / backup / version
internal/panel/       the single place where operations and permission checks live
internal/api/         HTTP routes, one file per area, registered in api.go
internal/mcp/         MCP tools on top of internal/panel
internal/store/       SQLite; migrations in store/migrations/NNNN_name.sql
internal/caddy/       builds the full Caddy config and loads it
internal/deploy/      releases of a site (unpack, versions, make live)
internal/dns/         provider-neutral DNS + hetzner/ (Cloud API only)
internal/secrets/     encryption at rest for provider tokens
internal/backup/      the backup command
internal/webui/       serves the embedded web app
web/                  React app (see web/AGENTS.md)
deploy/               systemd units, Caddy bootstrap, the root update worker
install.sh update.sh  install, upgrade, update and uninstall on a server
scripts/              dev.sh, smoke.sh, package.sh, commit-type.sh
wiki/                 documentation (synced to the GitHub wiki)
```

## Rules that are easy to break

- **Permissions are checked only in `internal/panel`.** Every service method
  takes the acting `Actor`; HTTP and MCP only translate. Never add a check
  in a handler instead, and never skip one because a handler "already
  checked". A resource someone may not see answers "not found".
- **Untrusted input:** uploaded archives, DNS answers, release downloads and
  anything from the network. Follow the existing patterns (`os.Root`, size
  limits, strict validation, no shelling out with user input).
- **Secrets** (passwords, access tokens, provider tokens) never appear in
  logs, activity entries, API responses after creation, or error messages.
- **Root stays out of the panel.** `ownpaneld` runs unprivileged. Anything
  that needs root goes through `install.sh`/`update.sh` and the validated
  request files of the update worker.
- **New dependencies** need a good reason; prefer the standard library. Pin
  GitHub Actions to commit SHAs with a version comment.
- **Migrations** are new files only; never edit one that has been released.
- **UI text** describes the effect for the person, never the technology
  (details in `web/AGENTS.md`).

## Commands

```sh
make test                      # go vet + go test
go test -race ./...            # what CI runs
cd web && npm ci && npm run lint && npm test -- --run && npm run build
scripts/dev.sh                 # Caddy + ownpaneld locally (8080/8443/8600)
INSECURE=1 scripts/smoke.sh    # end-to-end check against dev.sh
make build                     # binary with the web app embedded
scripts/package.sh <version>   # release archives as CI builds them
```

Node comes from nvm on the maintainer's machine: load it first
(`export NVM_DIR="$HOME/.nvm"; . "$NVM_DIR/nvm.sh"`).

Live tests run only with a build tag and credentials, e.g.
`HETZNER_TOKEN=… HETZNER_TEST_ZONE=… go test -tags live ./internal/dns/hetzner`
and `go test -tags caddy ./internal/caddy` (needs `bin/caddy`).

## Done means tested

Run the commands above for what you touched. For UI changes, run the app
(`make build`, then Caddy + `bin/ownpaneld` as in `scripts/dev.sh`) and click
through the flow in a browser, light and dark, desktop and 375 px. For
install and update changes, test in a systemd container. If something
could not be tested end to end, say so explicitly.

## Git, pull requests, releases

- `main` is protected: pull requests only, merged with a **merge commit**
  (no squash or rebase). Required checks: `go`, `web`, `lint`, `build`,
  `commits`. CI skips jobs whose files did not change.
- **Conventional Commits** for every commit on `main` and every PR title
  (`feat:`, `fix:`, `perf:`, `docs:`, `chore:`, `ci:`, `refactor:`,
  `test:`, `build:`, optional scope and `!`). The `commits` check enforces it.
- Every merge to `main` that changes shipped files publishes the **nightly**
  prerelease. Merging `main` into `release` publishes a **stable** release;
  its version comes from the commits (`fix:` patch, `feat:` minor, `!` major).
- Planned work is tracked as GitHub issues labelled `area: …` and `type: …`;
  reference the issue in the PR (`Closes #n`).
- Update the wiki (and README where it covers the same) in the same PR as
  the behaviour it describes.
