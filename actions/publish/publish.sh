#!/usr/bin/env bash
# Publishes a folder as a new version of an ownpanel site. This is the logic of
# the GitHub Action in action.yml, but it runs anywhere with bash, tar, curl
# and jq:
#
#   OWNPANEL_PANEL=https://panel.example.com OWNPANEL_TOKEN=op_... \
#   OWNPANEL_SITE=blog OWNPANEL_PATH=dist actions/publish/publish.sh
#
# OWNPANEL_TIMEOUT (seconds, default 300) limits the upload and the wait for
# the web server. In GitHub Actions the outputs "version" and "url" go to
# $GITHUB_OUTPUT and a summary to $GITHUB_STEP_SUMMARY.
#
# The API is synchronous: POST /api/sites/{name}/deployments unpacks the
# archive, switches the site to the new version and answers 201 with the site
# (current_release is the new version). Only a 502 "saved, but the web server
# could not be updated yet" leaves something to wait for; then the site's
# address is polled until it serves.
set -euo pipefail

# The panel refuses larger uploads (MaxUploadBytes in internal/api/api.go).
# OWNPANEL_MAX_BYTES exists for the tests only.
max_bytes=${OWNPANEL_MAX_BYTES:-536870912}

panel=${OWNPANEL_PANEL:-}
token=${OWNPANEL_TOKEN:-}
site=${OWNPANEL_SITE:-}
path=${OWNPANEL_PATH:-}
timeout=${OWNPANEL_TIMEOUT:-300}

in_actions() { [ "${GITHUB_ACTIONS:-}" = true ]; }

# Data of workflow commands must have %, CR and LF escaped, so text from the
# server cannot start a command of its own on a new line.
escape() {
  local s=$1
  s=${s//%/%25}
  s=${s//$'\r'/%0D}
  s=${s//$'\n'/%0A}
  printf '%s' "$s"
}

fail() {
  if in_actions; then
    printf '::error title=Publishing to ownpanel failed::%s\n' "$(escape "$*")"
  else
    printf 'error: %s\n' "$*" >&2
  fi
  exit 1
}

warn() {
  if in_actions; then
    printf '::warning title=ownpanel::%s\n' "$(escape "$*")"
  else
    printf 'warning: %s\n' "$*" >&2
  fi
}

for tool in tar curl jq; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is needed but not installed."
done

# --- Inputs -----------------------------------------------------------------

[ -n "$token" ] || fail "No access token given (input 'token'). Store a write token limited to this site as a repository secret, for example OWNPANEL_TOKEN, and pass it with token: \${{ secrets.OWNPANEL_TOKEN }}."
case $token in
  *[[:space:][:cntrl:]]*) fail "The access token contains spaces or line breaks; copy it again from the panel." ;;
esac
# Hide the token in the log even if it does not come from a secret.
if in_actions; then printf '::add-mask::%s\n' "$token"; fi

[ -n "$panel" ] || fail "No panel address given (input 'panel'), for example https://panel.example.com."
while [ "${panel%/}" != "$panel" ]; do panel=${panel%/}; done
if ! [[ $panel =~ ^https?://[^[:space:][:cntrl:]/?#]+(/[^[:space:][:cntrl:]?#]*)?$ ]]; then
  fail "The panel address must look like https://panel.example.com."
fi
case $panel in
  http://localhost | http://localhost:* | http://localhost/* | http://127.* | http://\[::1\]*) ;;
  http://*) warn "The panel address uses http://, so the access token travels unencrypted. Use https://." ;;
esac

[ -n "$site" ] || fail "No site name given (input 'site')."
if ! [[ $site =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]; then
  fail "'$site' is not a site name: use the name shown in the panel (lowercase letters, digits and hyphens)."
fi

[[ $timeout =~ ^[1-9][0-9]{0,4}$ ]] || fail "The timeout must be a number of seconds between 1 and 99999, not '$timeout'."

[ -n "$path" ] || fail "No folder given (input 'path'), for example dist."
[ -e "$path" ] || fail "The folder '$path' does not exist. Did the build step run, and does it write there?"
[ -d "$path" ] || fail "'$path' is not a folder. Give the folder with the built site, for example dist."
if [ -z "$(find -L "$path" -type f -print 2>/dev/null | head -n 1)" ]; then
  fail "The folder '$path' contains no files. Did the build step run?"
fi

# --- Pack -------------------------------------------------------------------

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
umask 077

archive="$work/site.tar.gz"
# -h stores the files that symbolic links point to (the panel refuses links).
# GNU tar can do the same for hard links; bsdtar (macOS) cannot.
tar_opts=(-c -z -h -f "$archive" -C "$path")
if tar --version 2>/dev/null | grep -q 'GNU tar'; then
  tar_opts=(--hard-dereference "${tar_opts[@]}")
fi
# COPYFILE_DISABLE keeps macOS from adding ._ files.
COPYFILE_DISABLE=1 tar "${tar_opts[@]}" . 2>"$work/tar.err" ||
  fail "Could not pack '$path': $(head -c 500 "$work/tar.err")"

size=$(wc -c <"$archive" | tr -d '[:space:]')
mb() { awk -v b="$1" 'BEGIN { if (b < 1048576) printf "%.0f KB", (b + 1023) / 1024; else printf "%.1f MB", b / 1048576 }'; }
if [ "$size" -gt "$max_bytes" ]; then
  fail "The packed site is $(mb "$size"), but the panel accepts at most $(mb "$max_bytes"). Leave out what is not needed on the web (source maps, original images)."
fi
echo "Packed '$path' ($(mb "$size") compressed)."

# --- Talk to the panel ------------------------------------------------------

# The token goes to curl in a header file, never on its command line.
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

# request <curl args...>: sets $code (000 on network errors) and leaves the
# body in $work/body and curl's error in $work/curl.err.
request() {
  code=$(curl -sS -o "$work/body" -w '%{http_code}' \
    --connect-timeout 30 --max-time "$timeout" \
    -H "@$work/auth" -H 'Accept: application/json' \
    "$@" 2>"$work/curl.err") || code=000
}

# message: the panel's error text, or a hint that the answer did not come
# from ownpanel at all. Control characters are dropped, length is limited.
message() {
  local m
  m=$(jq -r 'if type == "object" and (.error | type) == "string" then .error else empty end' "$work/body" 2>/dev/null || true)
  printf '%s' "$m" | tr -d '\000-\037' | head -c 500
}

network_error() {
  fail "Could not reach the panel at $panel: $(tr -d '\000-\037' <"$work/curl.err" | head -c 500). Check the address and that the panel is reachable from GitHub's runners."
}

echo "Uploading to site '$site' on $panel ..."
request -X POST -H 'Content-Type: application/octet-stream' \
  --data-binary "@$archive" "$panel/api/sites/$site/deployments"

msg=$(message)
not_synced=false
case $code in
  201) ;;
  000) network_error ;;
  400)
    case $msg in
      *"too large"*) fail "The panel refused the archive as too large ($(mb "$size")): $msg" ;;
      "") fail "The panel refused the archive (HTTP 400)." ;;
      *) fail "The panel refused the archive: $msg" ;;
    esac
    ;;
  401) fail "The access token was not accepted (HTTP 401): it is wrong, was deleted, or belongs to another panel. Create a new one in the panel and update the secret." ;;
  403) fail "This access token may not publish to '$site' (HTTP 403)${msg:+: $msg}. It needs write access and must include this site." ;;
  404 | 405)
    if [ "$code" = 405 ] || [ -z "$msg" ] || [ "$msg" = "No such API endpoint" ]; then
      fail "$panel does not answer like an ownpanel panel (HTTP $code). Check the 'panel' address: it is the address of the panel itself, not of the site."
    fi
    fail "Site '$site' was not found (HTTP 404). It does not exist, or the access token may not see it: a token limited to other sites gets the same answer. Check the site name and the token's sites."
    ;;
  413) fail "The archive ($(mb "$size")) is too large for the panel or a proxy in front of it (HTTP 413)." ;;
  502)
    case $msg in
      "Saved, but the web server could not be updated yet"*) not_synced=true ;;
      *) fail "The panel answered HTTP 502${msg:+: $msg}. A proxy in front of it may not reach it." ;;
    esac
    ;;
  *)
    [ -n "$msg" ] || fail "Publishing failed (HTTP $code), and the answer did not come from ownpanel. Check the 'panel' address."
    fail "Publishing failed (HTTP $code): $msg"
    ;;
esac

if [ "$not_synced" = true ]; then
  # The new version is saved and switched to, but the web server has not
  # been told about the site yet (only the first publish needs that). The
  # panel keeps retrying in the background; fetch the site for the version.
  warn "$msg"
  request "$panel/api/sites/$site"
  case $code in
    200) ;;
    000) network_error ;;
    *) fail "Could not read site '$site' after publishing (HTTP $code)." ;;
  esac
fi

version=$(jq -r '.current_release // empty' "$work/body" 2>/dev/null || true)
[[ $version =~ ^[A-Za-z0-9._-]+$ ]] || fail "The panel's answer contained no valid version; it may not be an ownpanel panel."
domains=()
while IFS= read -r d; do
  [[ $d =~ ^[a-z0-9.-]+$ ]] && domains+=("$d")
done < <(jq -r '.domains // [] | .[] | strings' "$work/body" 2>/dev/null || true)

url=""
if [ "${#domains[@]}" -gt 0 ]; then url="https://${domains[0]}"; fi

if [ "$not_synced" = true ]; then
  [ -n "$url" ] || fail "Version $version is saved, but the site has no domain to check whether it is served. $msg"
  # Wait until the address serves the site: not the "no content yet"
  # placeholder (503), not "Unknown site." (404) of a web server that does
  # not know the site yet, and not a missing certificate (000).
  deadline=$(($(date +%s) + timeout))
  echo "Version $version is saved; waiting up to ${timeout}s for $url to serve it ..."
  got=000
  while :; do
    left=$((deadline - $(date +%s)))
    [ "$left" -gt 0 ] || fail "Version $version is saved, but $url did not serve it within ${timeout}s (last answer: HTTP $got). The panel keeps trying to update the web server. $msg"
    got=$(curl -s -o /dev/null -w '%{http_code}' --max-time "$((left < 10 ? left : 10))" "$url/" 2>/dev/null) || got=000
    case $got in
      2?? | 3??) break ;;
      *) sleep "$((left < 5 ? left : 5))" ;;
    esac
  done
fi

# --- Report -----------------------------------------------------------------

if [ -n "$url" ]; then
  echo "Published version $version of '$site'; it is live at $url"
else
  echo "Published version $version of '$site'."
  warn "Site '$site' has no domains, so it is not served anywhere yet. Add a domain in the panel."
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  {
    echo "version=$version"
    echo "url=$url"
  } >>"$GITHUB_OUTPUT"
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Published to ownpanel"
    echo
    echo "| | |"
    echo "|---|---|"
    echo "| Site | \`$site\` |"
    echo "| Version | \`$version\` |"
    if [ "${#domains[@]}" -gt 0 ]; then
      links=""
      for d in "${domains[@]}"; do links="$links${links:+, }https://$d"; done
      echo "| Address | $links |"
    else
      echo "| Address | none: the site has no domains yet |"
    fi
    echo "| Size | $(mb "$size") compressed |"
  } >>"$GITHUB_STEP_SUMMARY"
fi
