#!/usr/bin/env bash
# Sends a push delivery, signed like GitHub signs it, from the development
# GitHub stand-in (go run ./internal/github/githubtest/fakegithub) to a
# panel, to see what a push does without a public address.
#
#   scripts/github-push.sh <fake GitHub URL> <panel URL> <owner/repo> [branch] [message] [delivery id]
#   scripts/github-push.sh http://127.0.0.1:9101 https://panel.localhost:8443 acme/website main "Fix the header"
#
# Sending it again with the same delivery id shows that the panel ignores
# it. Development only: certificates are not checked (-k).
set -euo pipefail

fake=${1:?fake GitHub URL}
panel=${2:?panel URL}
repo=${3:?owner/repo}
branch=${4:-main}
message=${5:-Change from the development stand-in}
delivery=${6:-}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsS -G -D "$tmp/headers" -o "$tmp/body" "$fake/_dev/push" \
  --data-urlencode "repo=$repo" --data-urlencode "branch=$branch" \
  --data-urlencode "message=$message" --data-urlencode "delivery=$delivery"

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

curl -ksS -X POST --data-binary "@$tmp/body" "${args[@]}" "$panel/api/github/webhook"
echo
