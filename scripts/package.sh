#!/usr/bin/env bash
# Builds the release archives. The release workflow and local builds both use
# this script, so the contents of a release are defined only here.
#
#   scripts/package.sh <version> [arch ...]      arch: amd64, arm64 (default: both)
#
# Writes to dist/:
#   ownpanel_<version>_linux_<arch>.tar.gz   one directory ownpanel_<version>_linux_<arch>/ with
#                                            ownpaneld, caddy, install.sh, update.sh, deploy/
#                                            (units, Caddy bootstrap, the updater's worker),
#                                            LICENSE, README.md, VERSION
#   checksums.txt                            SHA-256 of the archives (sha256sum format)
#
# SKIP_WEB=1 reuses an existing build of the web app in internal/webui/dist.
# OWNPANEL_REPO (default danielgtmn/ownpanel) is the GitHub repository the
# panel looks for new releases in.
set -euo pipefail
cd "$(dirname "$0")/.."

# Caddy comes from its official release archives instead of being compiled
# here: they are the builds the Caddy project tests and publishes, they do not
# depend on our Go version, and packaging stays fast. Each archive is checked
# against a SHA-512 pinned below, so a changed download fails the build.
# To update: set CADDY_VERSION and copy the two linux_amd64/linux_arm64 lines from
# https://github.com/caddyserver/caddy/releases/download/v<version>/caddy_<version>_checksums.txt
CADDY_VERSION=2.11.9
caddy_sha512() {
  case $1 in
    amd64) echo 0e85544340290a16ce4bfb76fe7d65e9de1bf3a6e67f5ccfff30b30d27ea4c73e078077b7bf3e58ae60b6b12e44327705287059c775bca5782e8ee85ea7edd75 ;;
    arm64) echo 9e5e76d7888542924de2c25353ae87c5f77e2a22c5b1198633cae13688922282e05383d142599205c88d8d80eee5e967d291214d7abce4835329a191b20dd6e0 ;;
  esac
}

die() { echo "package.sh: $*" >&2; exit 1; }

[ $# -ge 1 ] || die "usage: scripts/package.sh <version> [amd64|arm64 ...]"
version=$1
shift
repo=${OWNPANEL_REPO:-danielgtmn/ownpanel}
[[ $repo =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "OWNPANEL_REPO '$repo' is not owner/name"
[[ $version =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "version '$version' may only contain letters, digits, '.', '_' and '-'"
arches=("$@")
[ ${#arches[@]} -gt 0 ] || arches=(amd64 arm64)
for arch in "${arches[@]}"; do
  [ -n "$(caddy_sha512 "$arch")" ] || die "unsupported architecture '$arch' (use amd64 or arm64)"
done

sha() { # sha <bits> <file>: prints "<hash>  <file>"
  if command -v "sha$1sum" >/dev/null; then "sha$1sum" "$2"; else shasum -a "$1" "$2"; fi
}

dist=dist
mkdir -p "$dist/.caddy"
rm -rf "$dist"/ownpanel_* "$dist/checksums.txt"

if [ "${SKIP_WEB:-0}" != 1 ]; then
  # Node may come from nvm (developer machines); CI puts it on PATH.
  if ! command -v node >/dev/null && [ -s "${NVM_DIR:-$HOME/.nvm}/nvm.sh" ]; then
    # shellcheck disable=SC1091
    . "${NVM_DIR:-$HOME/.nvm}/nvm.sh"
  fi
  command -v node >/dev/null || die "node is needed to build the web app (or set SKIP_WEB=1)"
  (cd web && npm ci --no-audit --no-fund && npm run build)
fi
[ -f internal/webui/dist/index.html ] || die "internal/webui/dist is empty: build the web app first"

# GNU tar and bsdtar (macOS) spell "files belong to root" differently.
if tar --version 2>/dev/null | grep -q GNU; then
  tar_owner=(--owner=0 --group=0 --numeric-owner)
else
  tar_owner=(--uid 0 --gid 0 --no-mac-metadata --no-xattrs)
fi

for arch in "${arches[@]}"; do
  name="ownpanel_${version}_linux_${arch}"
  stage="$dist/$name"
  echo "== $name"
  mkdir -p "$stage/deploy"

  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -tags webui -trimpath \
    -ldflags "-s -w -X main.version=$version -X main.repo=$repo" -o "$stage/ownpaneld" ./cmd/ownpaneld

  caddy_tgz="$dist/.caddy/caddy_${CADDY_VERSION}_linux_${arch}.tar.gz"
  if [ ! -f "$caddy_tgz" ]; then
    curl -fsSL --retry 3 -o "$caddy_tgz.part" \
      "https://github.com/caddyserver/caddy/releases/download/v${CADDY_VERSION}/caddy_${CADDY_VERSION}_linux_${arch}.tar.gz"
    mv "$caddy_tgz.part" "$caddy_tgz"
  fi
  got=$(sha 512 "$caddy_tgz" | cut -d' ' -f1)
  if [ "$got" != "$(caddy_sha512 "$arch")" ]; then
    rm -f "$caddy_tgz"
    die "checksum mismatch for Caddy $CADDY_VERSION ($arch): got $got"
  fi
  tar -xzf "$caddy_tgz" -C "$stage" caddy

  cp deploy/ownpaneld.service deploy/ownpanel-caddy.service deploy/ownpanel-mail.service deploy/caddy-bootstrap.json \
    deploy/ownpanel-update deploy/ownpanel-update.service deploy/ownpanel-update.path deploy/ownpanel-update.timer \
    deploy/ownpanel-build-setup deploy/ownpanel-build-setup.service deploy/ownpanel-build.service \
    deploy/nftables-ownpanel-build.conf \
    "$stage/deploy/"
  cp install.sh update.sh LICENSE README.md "$stage/"
  echo "$version" > "$stage/VERSION"
  chmod 0644 "$stage"/deploy/* "$stage/LICENSE" "$stage/README.md" "$stage/VERSION"
  chmod 0755 "$stage/ownpaneld" "$stage/caddy" "$stage/install.sh" "$stage/update.sh" "$stage/deploy" \
    "$stage/deploy/ownpanel-update" "$stage/deploy/ownpanel-build-setup"

  COPYFILE_DISABLE=1 tar "${tar_owner[@]}" -czf "$dist/$name.tar.gz" -C "$dist" "$name"
  rm -rf "$stage"
done

(cd "$dist" && for f in ownpanel_*.tar.gz; do sha 256 "$f"; done) > "$dist/checksums.txt"
echo "== $dist/checksums.txt"
cat "$dist/checksums.txt"
