#!/usr/bin/env bash
# Runs Caddy and ownpaneld locally on unprivileged ports with self-signed
# certificates. Everything is kept inside ./data; Caddy's admin API is a Unix
# socket there. Stop with Ctrl-C.
set -euo pipefail
cd "$(dirname "$0")/.."

[ -x bin/caddy ] || GOBIN="$PWD/bin" go install github.com/caddyserver/caddy/v2/cmd/caddy@latest
# No -tags webui: in development the web app runs from the Vite dev server.
go build -o bin/ownpaneld ./cmd/ownpaneld

mkdir -p data
# The socket is 0660 like in production; the private directory keeps other
# local users away from it.
chmod 700 data
export XDG_DATA_HOME="$PWD/data/caddy-home" XDG_CONFIG_HOME="$PWD/data/caddy-home"

sock="$PWD/data/caddy-admin.sock"
# Unix socket paths are limited to about 104 bytes on macOS.
if [ "${#sock}" -gt 100 ]; then
  echo "path of $sock is too long for a Unix socket; move the checkout to a shorter path" >&2
  exit 1
fi
printf '{"admin":{"listen":"unix/%s|0660"}}\n' "$sock" > data/caddy-bootstrap.json

bin/caddy run --config data/caddy-bootstrap.json --resume &
caddy_pid=$!
trap 'kill $caddy_pid 2>/dev/null || true' EXIT

bin/ownpaneld -data ./data -tls internal -http-port 8080 -https-port 8443 -caddy-admin "unix://$sock" "$@"
