#!/usr/bin/env bash
# Runs actions/publish/publish.sh against a real ownpaneld. Caddy's admin API
# is replaced by a small stub that accepts every config, so no web server is
# needed; what visitors see is covered by scripts/smoke.sh.
#
#   actions/publish/publish_test.sh                    # builds ownpaneld with go
#   OWNPANELD=bin/ownpaneld actions/publish/publish_test.sh
#
# Needs bash, curl, jq, tar and python3 (for the stub).
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
repo=$PWD
script="$repo/actions/publish/publish.sh"

work=$(mktemp -d)
pids=()
cleanup() {
  for p in ${pids[@]+"${pids[@]}"}; do kill "$p" 2>/dev/null && wait "$p" 2>/dev/null; done
  rm -rf "$work"
}
trap cleanup EXIT

fails=0
ok() { echo "ok    $*"; }
bad() { echo "FAIL  $*"; fails=$((fails + 1)); }
die() { echo "FAIL  $*"; exit 1; }
check() { # check <description> <command...>: ok if the command succeeds
  local what=$1
  shift
  if "$@"; then ok "$what"; else bad "$what"; fi
}

if [ -z "${OWNPANELD:-}" ]; then
  go build -o "$work/ownpaneld" ./cmd/ownpaneld || die "could not build ownpaneld"
  OWNPANELD="$work/ownpaneld"
fi

free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
wait_for() { # wait_for <url>
  for _ in $(seq 100); do
    curl -s -o /dev/null "$1" && return 0
    sleep 0.1
  done
  return 1
}

# --- A panel with a stubbed web server --------------------------------------

stub_port=$(free_port)
cat >"$work/stub.py" <<'EOF'
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length") or 0))
        self.send_response(200); self.end_headers()
    def do_GET(self):
        self.send_response(200); self.end_headers()
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
EOF
python3 -I "$work/stub.py" "$stub_port" &
pids+=($!)

panel_port=$(free_port)
panel="http://127.0.0.1:$panel_port"
mkdir -p "$work/data"
"$OWNPANELD" serve -data "$work/data" -listen "127.0.0.1:$panel_port" -tls off \
  -http-port 18080 -https-port 18443 -caddy-admin "http://127.0.0.1:$stub_port" \
  >"$work/ownpaneld.log" 2>&1 &
pids+=($!)
wait_for "$panel/api/session" || { cat "$work/ownpaneld.log"; die "ownpaneld did not start"; }

cookies="$work/cookies"
admin() { curl -sS --fail-with-body -b "$cookies" -c "$cookies" -H "X-Ownpanel-Request: 1" "$@"; }
code=$(tr -d '[:space:]' <"$work/data/setup-code")
admin -o /dev/null -X POST "$panel/api/setup" \
  -d "$(jq -n --arg c "$code" '{setup_code: $c, name: "admin", password: "publish-test-password-1"}')" ||
  die "setup failed"
admin -o /dev/null -X PUT "$panel/api/sites/blog" -d '{"domains": ["blog.example.test", "www.blog.example.test"]}' || die "create blog"
admin -o /dev/null -X PUT "$panel/api/sites/other" -d '{"domains": ["other.example.test"]}' || die "create other"
admin -o /dev/null -X PUT "$panel/api/sites/nodomain" -d '{"domains": []}' || die "create nodomain"
new_token() { # new_token <access> <sites json>
  admin -X POST "$panel/api/tokens" -d "{\"name\": \"test $RANDOM\", \"access\": \"$1\", \"sites\": $2}" | jq -r .token
}
blog_token=$(new_token write '["blog"]')
nodomain_token=$(new_token write '["nodomain"]')
read_token=$(new_token read '["blog"]')
[ -n "$blog_token" ] && [ "$blog_token" != null ] || die "no token"

# --- Helpers ----------------------------------------------------------------

mkdir -p "$work/dist/assets" "$work/empty"
echo '<h1>hello</h1>' >"$work/dist/index.html"
echo 'body{}' >"$work/dist/assets/app.css"
ln -s app.css "$work/dist/assets/linked.css"

# publish [VAR=value ...]: runs the script like action.yml does, as GitHub
# Actions would. Sets $status, $out (stdout+stderr), $outputs, $summary.
publish() {
  : >"$work/github_output"
  : >"$work/summary"
  out=$(cd "$work" && env GITHUB_ACTIONS=true GITHUB_OUTPUT="$work/github_output" \
    GITHUB_STEP_SUMMARY="$work/summary" OWNPANEL_PANEL="$panel" \
    OWNPANEL_TOKEN="$blog_token" OWNPANEL_SITE=blog OWNPANEL_PATH=dist OWNPANEL_TIMEOUT=60 \
    "$@" bash "$script" 2>&1)
  status=$?
  outputs=$(cat "$work/github_output")
  summary=$(cat "$work/summary")
}
# expect_fail <description> <text the error must contain> [VAR=value ...]
expect_fail() {
  local what=$1 want=$2
  shift 2
  publish "$@"
  if [ "$status" = 0 ]; then
    bad "$what: succeeded"
  elif ! grep -q '^::error' <<<"$out"; then
    bad "$what: no ::error line in: $out"
  elif ! grep -qF -- "$want" <<<"$out"; then
    bad "$what: '$want' not in: $out"
  elif [ -n "$outputs" ]; then
    bad "$what: wrote outputs: $outputs"
  else
    ok "$what"
  fi
}
no_token_in_log() { # the only place the token may appear is the mask command
  if grep -v '^::add-mask::' <<<"$out" | grep -qF -- "$1"; then bad "token printed: $out"; fi
}

# --- Success ----------------------------------------------------------------

publish
version=$(sed -n 's/^version=//p' <<<"$outputs")
url=$(sed -n 's/^url=//p' <<<"$outputs")
current=$(admin "$panel/api/sites/blog" | jq -r .current_release)
if [ "$status" != 0 ]; then
  bad "publish: exit $status: $out"
elif [ -z "$version" ] || [ "$version" != "$current" ]; then
  bad "publish: version output '$version', live version '$current'"
elif [ "$url" != "https://blog.example.test" ]; then
  bad "publish: url output '$url'"
elif ! grep -qF "$version" <<<"$summary" || ! grep -qF "https://www.blog.example.test" <<<"$summary"; then
  bad "publish: summary: $summary"
elif ! grep -qF "it is live at https://blog.example.test" <<<"$out"; then
  bad "publish: log: $out"
else
  ok "publish sets version $version and url $url"
fi
no_token_in_log "$blog_token"
check "token is masked" grep -qxF "::add-mask::$blog_token" <<<"$out"

check "files including a followed symbolic link are live" \
  test -f "$work/data/sites/blog/releases/$version/index.html" -a \
  -f "$work/data/sites/blog/releases/$version/assets/app.css" -a \
  ! -L "$work/data/sites/blog/releases/$version/assets/linked.css" -a \
  -f "$work/data/sites/blog/releases/$version/assets/linked.css"

publish OWNPANEL_PANEL="$panel/"
v2=$(sed -n 's/^version=//p' <<<"$outputs")
if [ "$status" = 0 ] && [ -n "$v2" ] && [ "$v2" != "$version" ]; then
  ok "second publish with trailing slash gives a new version"
else
  bad "second publish: $out"
fi

publish OWNPANEL_TOKEN="$nodomain_token" OWNPANEL_SITE=nodomain
if [ "$status" = 0 ] && grep -qx 'url=' <<<"$outputs" && grep -q '^::warning.*no domains' <<<"$out"; then
  ok "site without domains: empty url and a warning"
else
  bad "site without domains: $status $out / $outputs"
fi

# --- Failures ---------------------------------------------------------------

expect_fail "wrong token" "HTTP 401" OWNPANEL_TOKEN=op_wrongwrongwrong
no_token_in_log op_wrongwrongwrong
expect_fail "unknown site" "Site 'nope' was not found" OWNPANEL_SITE=nope
expect_fail "site the token may not see" "Site 'other' was not found" OWNPANEL_SITE=other
expect_fail "read-only token" "HTTP 403" OWNPANEL_TOKEN="$read_token"
expect_fail "missing folder" "The folder 'build' does not exist" OWNPANEL_PATH=build
expect_fail "empty folder" "contains no files" OWNPANEL_PATH=empty
expect_fail "file instead of folder" "is not a folder" OWNPANEL_PATH=dist/index.html
expect_fail "no path" "input 'path'" OWNPANEL_PATH=
expect_fail "no token" "input 'token'" OWNPANEL_TOKEN=
expect_fail "no site" "input 'site'" OWNPANEL_SITE=
expect_fail "invalid site name" "is not a site name" OWNPANEL_SITE='../tokens'
expect_fail "no panel" "input 'panel'" OWNPANEL_PANEL=
expect_fail "panel is not a URL" "must look like" OWNPANEL_PANEL=panel.example.com
expect_fail "bad timeout" "timeout must be" OWNPANEL_TIMEOUT=soon
expect_fail "too large" "accepts at most" OWNPANEL_MAX_BYTES=10
expect_fail "panel not reachable" "Could not reach the panel" OWNPANEL_PANEL="http://127.0.0.1:$(free_port)"
expect_fail "address is not the panel" "does not answer like an ownpanel panel" OWNPANEL_PANEL="$panel/nested"

after=$(admin "$panel/api/sites/blog" | jq -r .current_release)
check "failed attempts changed nothing (live: $after)" test "$after" = "$v2"

if [ "$fails" -gt 0 ]; then
  echo "$fails failed"
  exit 1
fi
echo "all passed"
