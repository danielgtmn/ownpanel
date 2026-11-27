#!/usr/bin/env bash
# Changes a pull request at the development GitHub stand-in (go run
# ./internal/github/githubtest/fakegithub) and sends its pull_request
# delivery, signed like GitHub signs it, to a panel, to see what pull
# requests do without a public address.
#
#   scripts/github-pull.sh <fake GitHub URL> <panel URL> <owner/repo> opened <head branch> [base branch] [fork owner/repo]
#   scripts/github-pull.sh <fake GitHub URL> <panel URL> <owner/repo> synchronize|closed|reopened <number>
#
#   scripts/github-pull.sh http://127.0.0.1:9101 https://panel.localhost:8443 acme/website opened dev
#   scripts/github-pull.sh http://127.0.0.1:9101 https://panel.localhost:8443 acme/website synchronize 1
#
# The head branch needs a commit from /_dev/commit first (for a fork, a
# commit of the fork's branch). "opened" prints the pull request's number
# from the delivery. Development only: certificates are not checked (-k).
set -euo pipefail

fake=${1:?fake GitHub URL}
panel=${2:?panel URL}
repo=${3:?owner/repo}
action=${4:?opened, synchronize, closed or reopened}

query=(--data-urlencode "repo=$repo" --data-urlencode "action=$action")
case $action in
  opened)
    query+=(--data-urlencode "head=${5:?head branch}" --data-urlencode "base=${6:-main}")
    if [ -n "${7:-}" ]; then query+=(--data-urlencode "fork=$7"); fi
    ;;
  *) query+=(--data-urlencode "number=${5:?pull request number}") ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsS -G -D "$tmp/headers" -o "$tmp/body" "$fake/_dev/pull_request" "${query[@]}"

args=()
while IFS= read -r line; do
  line=${line%$'\r'}
  case $line in
    X-Dev-*:*)
      name=${line%%:*}
      value=${line#*: }
      args+=(-H "${name#X-Dev-}: $value")
      ;;
  esac
done < "$tmp/headers"

if [ "$action" = opened ]; then
  n=$(sed -n 's/.*"number":\([0-9]*\),"pull_request".*/\1/p' "$tmp/body")
  echo "pull request ${n:-?}"
fi
curl -ksS -X POST --data-binary "@$tmp/body" "${args[@]}" "$panel/api/github/webhook"
echo
