#!/usr/bin/env bash
# End-to-end check against a running ownpaneld + Caddy:
# sign in -> create site -> deploy -> fetch over HTTPS -> redeploy -> rollback
# -> delete, plus checks that the API refuses what it must refuse.
#
#   API=http://127.0.0.1:8600 DOMAIN=demo.example.test WEB=127.0.0.1 \
#   HTTP_PORT=8080 HTTPS_PORT=8443 INSECURE=1 scripts/smoke.sh
#
# WEB is the address the web server is reached at (DOMAIN is pinned to it, so
# no DNS record is needed). INSECURE=1 accepts self-signed certificates.
#
# Authentication, first match wins:
#   OWNPANEL_TOKEN=op_...            use this access token
#   SETUP_CODE_FILE (./data/setup-code) exists: create the first admin
#                                  ADMIN_NAME (admin) with a generated password
#   ADMIN_NAME + ADMIN_PASSWORD    sign in
# With a password session the script creates a temporary access token for
# the checks and deletes it at the end.
set -euo pipefail

API=${API:-http://127.0.0.1:8600}
DOMAIN=${DOMAIN:-demo.example.test}
WEB=${WEB:-127.0.0.1}
HTTP_PORT=${HTTP_PORT:-8080}
HTTPS_PORT=${HTTPS_PORT:-8443}
SITE=${SITE:-smoke-demo}
CERT_WAIT=${CERT_WAIT:-60}
SETUP_CODE_FILE=${SETUP_CODE_FILE:-./data/setup-code}
ADMIN_NAME=${ADMIN_NAME:-admin}

tls=()
[ "${INSECURE:-0}" = 1 ] && tls=(-k)

work=$(mktemp -d)
cookies="$work/cookies"
session=0  # 1 once signed in with a password (cookie in $cookies)
token_id=""

# with_session: curl with the session cookie and the header the API demands
# from cookie-authenticated changes.
with_session() { curl -sS -b "$cookies" -c "$cookies" -H "X-Ownpanel-Request: 1" "$@"; }

cleanup() {
  if [ -n "$token_id" ]; then
    with_session -o /dev/null -X DELETE "$API/api/tokens/$token_id" || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL $*"; exit 1; }
expect() { # expect <description> <actual> <wanted>
  [ $# = 3 ] || fail "expect: $# arguments for '$1'"
  if [ "$2" = "$3" ]; then echo "ok   $1"; else fail "$1: got '$2', wanted '$3'"; fi
}
json_field() { # json_field <name> <json> -> value of a top-level string or number field
  printf '%s' "$2" | sed -n "s/.*\"$1\":\"\{0,1\}\([^\",}]*\)\"\{0,1\}.*/\1/p"
}
status() { curl -sS -o "$work/body" -w '%{http_code}' "$@"; }

step "unauthenticated request is refused"
expect "GET /api/sites without credentials" "$(status "$API/api/sites")" 401

if [ -n "${OWNPANEL_TOKEN:-}" ]; then
  token=$OWNPANEL_TOKEN
else
  if [ -f "$SETUP_CODE_FILE" ]; then
    step "first-run setup"
    ADMIN_PASSWORD=${ADMIN_PASSWORD:-$(head -c 18 /dev/urandom | base64 | tr -d '/+=')}
    code=$(tr -d '[:space:]' < "$SETUP_CODE_FILE")
    # Bodies go through variables: bash 3.2 mangles quotes nested in "$(...)".
    body="{\"setup_code\":\"$code\",\"name\":\"$ADMIN_NAME\",\"password\":\"$ADMIN_PASSWORD\"}"
    got=$(status -c "$cookies" -X POST "$API/api/setup" -d "$body")
    expect "setup creates the first admin" "$got" 201
    echo "     sign in to the panel as $ADMIN_NAME with password $ADMIN_PASSWORD"
    expect "setup code file is gone" "$([ -f "$SETUP_CODE_FILE" ] && echo present || echo gone)" gone
  else
    [ -n "${ADMIN_PASSWORD:-}" ] || fail "no credentials: set OWNPANEL_TOKEN, ADMIN_NAME and ADMIN_PASSWORD, or SETUP_CODE_FILE"
    step "sign in"
    body="{\"name\":\"$ADMIN_NAME\",\"password\":\"$ADMIN_PASSWORD\"}"
    got=$(status -c "$cookies" -X POST "$API/api/login" -d "$body")
    expect "login" "$got" 200
  fi
  session=1

  step "create a temporary access token"
  resp=$(with_session --fail-with-body -X POST "$API/api/tokens" -d '{"name":"smoke test","access":"write","sites":null}')
  token=$(json_field token "$resp")
  token_id=$(json_field id "$resp")
  [ -n "$token" ] && [ -n "$token_id" ] || fail "no token in $resp"
  echo "ok   token $token_id created"
fi

auth=(-H "Authorization: Bearer $token")
api() { curl -sS --fail-with-body "${auth[@]}" "$@"; }
web() {
  curl -sS ${tls[@]+"${tls[@]}"} --resolve "$DOMAIN:$HTTPS_PORT:$WEB" --resolve "$DOMAIN:$HTTP_PORT:$WEB" "$@"
}
pack() { # pack <label> -> path of a tar.gz whose index.html contains the label
  mkdir -p "$work/$1/assets"
  echo "<h1>$1</h1>" > "$work/$1/index.html"
  echo "body{}" > "$work/$1/assets/app.css"
  tar -czf "$work/$1.tar.gz" -C "$work/$1" .
  echo "$work/$1.tar.gz"
}
url="https://$DOMAIN:$HTTPS_PORT"

if [ "$session" = 1 ]; then
  step "the API refuses what it must refuse"
  expect "cookie request without X-Ownpanel-Request" \
    "$(status -b "$cookies" -X PUT "$API/api/sites/smoke-csrf" -d '{"domains":[]}')" 403
  resp=$(with_session --fail-with-body -X POST "$API/api/tokens" -d '{"name":"smoke read-only","access":"read","sites":null}')
  read_token=$(json_field token "$resp")
  read_id=$(json_field id "$resp")
  expect "read-only token cannot create a site" \
    "$(status -H "Authorization: Bearer $read_token" -X PUT "$API/api/sites/smoke-readonly" -d '{"domains":[]}')" 403
  expect "read-only token can read" "$(status -H "Authorization: Bearer $read_token" "$API/api/sites")" 200
  expect "delete read-only token" "$(status -b "$cookies" -H "X-Ownpanel-Request: 1" -X DELETE "$API/api/tokens/$read_id")" 204
  expect "deleted token is refused" "$(status -H "Authorization: Bearer $read_token" "$API/api/sites")" 401
else
  step "skipping cookie and token checks (OWNPANEL_TOKEN given, no password session)"
fi

step "create site"
api -X DELETE "$API/api/sites/$SITE" >/dev/null 2>&1 || true
api -X PUT "$API/api/sites/$SITE" -d "{\"domains\":[\"$DOMAIN\"]}"

step "wait for certificate, expect placeholder"
code=000
for _ in $(seq "$CERT_WAIT"); do
  code=$(web -o /dev/null -w '%{http_code}' "$url/" 2>/dev/null || true)
  [ "$code" = 503 ] && break
  sleep 1
done
expect "site without content answers 503" "$code" 503

step "deploy v1"
api -X POST "$API/api/sites/$SITE/deployments" --data-binary @"$(pack v1)"
expect "v1 is served" "$(web "$url/")" "<h1>v1</h1>"
expect "nested file is served" "$(web -o /dev/null -w '%{http_code}' "$url/assets/app.css")" 200
expect "missing file answers 404" "$(web -o /dev/null -w '%{http_code}' "$url/nope")" 404

step "http redirects to https"
expect "redirect status" "$(web -o /dev/null -w '%{http_code}' "http://$DOMAIN:$HTTP_PORT/")" 308

step "deploy v2"
api -X POST "$API/api/sites/$SITE/deployments" --data-binary @"$(pack v2)"
expect "v2 is served" "$(web "$url/")" "<h1>v2</h1>"

step "rollback"
api -X POST "$API/api/sites/$SITE/rollback"
expect "v1 is served again" "$(web "$url/")" "<h1>v1</h1>"

step "malicious archive is rejected"
mkdir -p "$work/evil" && echo x > "$work/evil/x"
ln -s /etc/passwd "$work/evil/link"
tar -czf "$work/evil.tar.gz" -C "$work/evil" .
code=$(status "${auth[@]}" -X POST "$API/api/sites/$SITE/deployments" --data-binary @"$work/evil.tar.gz")
expect "archive with a link is refused" "$code" 400
expect "v1 is still served" "$(web "$url/")" "<h1>v1</h1>"

if [ "${KEEP:-0}" = 1 ]; then
  step "keeping site (KEEP=1)"
else
  step "delete site"
  api -X DELETE "$API/api/sites/$SITE"
  code=$(web -o /dev/null -w '%{http_code}' "$url/" 2>/dev/null || true)
  case "$code" in 000|404) echo "ok   site is gone ($code)";; *) fail "site still answers $code";; esac
fi

printf '\nALL CHECKS PASSED\n'
