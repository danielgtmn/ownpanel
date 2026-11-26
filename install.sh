#!/usr/bin/env bash
# Installs ownpanel (ownpaneld, its mail service and Caddy as systemd services) on Debian 12+ or
# Ubuntu 22.04+ (amd64, arm64), and the isolated build environment for sites (rootless
# Podman, Debian 12+ and Ubuntu 24.04+; see setup_builds). Run it again to upgrade in place.
#
#   curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh | sudo bash
#   curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh \
#     | sudo bash -s -- --panel-domain panel.example.com --server-ip 203.0.113.5
#
# The script is self-contained, so it can be served from any address; releases
# always come from GitHub and are checked against the release's checksums.txt.
# See --help for all options. Everything runs from main() on the last line, so
# a download that breaks off never runs half a script.
#
# update.sh (next to this file, and installed to /usr/local/lib/ownpanel)
# loads this file with OWNPANEL_SOURCE_ONLY=1 to reuse the release lookup,
# download and checks, and runs the installer of the new release to install
# it: the file layout is only known here.
set -euo pipefail

REPO=${OWNPANEL_REPO:-danielgtmn/ownpanel}
# Releases are looked up and downloaded through GitHub's API (tests point
# this at a local fake).
API=${OWNPANEL_API_URL:-https://api.github.com}
# Every request to GitHub gives up on a connection that does not open, or
# that stalls (less than 1 KB/s for 30 seconds), instead of hanging until the
# total limit. A download that cannot finish must fail long before the
# updater's 30 minutes (ownpanel-update.service) are up.
CURL_LIMITS=(--connect-timeout 15 --speed-limit 1024 --speed-time 30)
# How often and how long to wait for a nightly that is being replaced.
NIGHTLY_LOOKUPS=6
NIGHTLY_WAIT=20

BIN=/usr/local/bin
LIB=/usr/local/lib/ownpanel
UNITS=/etc/systemd/system
ETC=/etc/ownpanel
DATA=/var/lib/ownpanel
ENV_FILE=$ETC/ownpaneld.env
RELEASE_FILE=$ETC/release
SETUP_CODE=$DATA/setup-code
# $DATA belongs to the panel's user, so root never creates, changes or reads
# anything by path in it during an install or update: a name there may be a
# link the panel put in place. What root manages lives in directories whose
# parents root owns.
# The updater's directory (root, 0755): its private state, the backups made
# before each update, and the requests from the panel (see ownpanel-update).
UPDATE_ROOT=/var/lib/ownpanel-update
UPDATE_DIR=$UPDATE_ROOT/requests
BACKUP_DIR=$UPDATE_ROOT/backups
# Caddy's own directory (caddy, 0700): certificates in storage, its home with
# the last config in home.
CADDY_DATA=/var/lib/ownpanel-caddy
MAIL_DATA=/var/lib/ownpanel-mail
MAIL_ADMIN_ARG="-mail-admin unix:///run/ownpanel-mail/admin.sock"
# The mail service's web socket, which Caddy passes the mail host's account
# pages to (see ownpanel-mail.service).
MAIL_WEB_ARG="-mail-web unix:///run/ownpanel-mail-web/web.sock"
MAIL_WEB_GROUP=ownpanel-mail-web
BUILD_WORKER_ARG="-build-worker unix:///run/ownpanel-build/worker.sock"
DEFAULT_ARGS="-tls acme -caddy-admin unix:///run/ownpanel/caddy-admin.sock $MAIL_ADMIN_ARG $MAIL_WEB_ARG $BUILD_WORKER_ARG"
# Builds (see setup_builds). The build user's directory (root, 0755) holds
# its home and the build disk; the settings and install.sh's record of what
# it set up are in $ETC.
BUILD_USER=ownpanel-build
BUILD_BASE=/var/lib/ownpanel-build
BUILD_HOME=$BUILD_BASE/home
BUILD_ENV=$ETC/build.env
BUILD_STATE=$ETC/builds
BUILD_UNITS=(ownpanel-build-setup.service ownpanel-build.service)
NFT_DROPIN_DIR=$UNITS/nftables.service.d
NFT_DROPIN=$NFT_DROPIN_DIR/ownpanel-build.conf
BUILD_PACKAGES=(podman uidmap slirp4netns nftables quota e2fsprogs util-linux)
SERVICES=(ownpanel-caddy ownpanel-mail ownpaneld)
UPDATE_UNITS=(ownpanel-update.service ownpanel-update.path ownpanel-update.timer)
# Files of a release, relative to its directory.
RELEASE_FILES=(ownpaneld caddy VERSION install.sh update.sh
  deploy/ownpaneld.service deploy/ownpanel-caddy.service deploy/ownpanel-mail.service deploy/caddy-bootstrap.json
  deploy/ownpanel-update deploy/ownpanel-update.service deploy/ownpanel-update.path deploy/ownpanel-update.timer
  deploy/ownpanel-build-setup deploy/ownpanel-build-setup.service deploy/ownpanel-build.service
  deploy/nftables-ownpanel-build.conf)

usage() {
  cat <<'EOF'
Install, upgrade or remove ownpanel.

  install.sh [options]

Options:
  --channel stable|nightly  release channel: stable (default) follows tagged releases,
                            nightly the automatic build of main. Upgrades keep the channel.
  --version vX.Y.Z          install exactly this release (wins over --channel)
  --panel-domain HOST       serve the panel on https://HOST (point HOST at this server)
  --server-ip ADDR          public IPv4/IPv6 address domains should point at; repeatable
  --acme-email ADDR         contact address for the certificate authority
                            These three are optional: without them, set them in the
                            panel's first-run setup in the browser.
  --from-dir DIR            install from an unpacked release directory instead of downloading
  --no-builds               do not set up building sites on this server (kept on upgrades)
  --builds                  set it up again after --no-builds
  --build-disk SIZE         space reserved for builds, e.g. 20G (default: 15% of the disk,
                            8G to 32G)
  -y, --yes                 do not ask for confirmation (only --purge asks)
  --uninstall               stop and remove services and binaries; keeps data and settings
  --purge                   with --uninstall: also delete /var/lib/ownpanel, /var/lib/ownpanel-mail
                            (all stored mail), /etc/ownpanel, the users ownpanel,
                            ownpanel-mail, ownpanel-build and caddy and the group
                            ownpanel-mail-web
  -h, --help                show this help

Options given on an upgrade replace the stored ones; everything else in
/etc/ownpanel/ownpaneld.env is kept.
EOF
}

say() { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
warn() { printf 'Warning: %s\n' "$*" >&2; }
die() { printf 'Error: %s\n' "$*" >&2; exit 1; }

# Options.
opt_channel=""
opt_version=""
opt_panel_domain=""
opt_server_ips=()
opt_acme_email=""
opt_from_dir=""
opt_yes=0
opt_uninstall=0
opt_purge=0
opt_builds=""
opt_build_disk=""

parse_args() {
  while [ $# -gt 0 ]; do
    case $1 in
      --channel) need_value "$@"; opt_channel=$2; shift ;;
      --version) need_value "$@"; opt_version=$2; shift ;;
      --panel-domain) need_value "$@"; opt_panel_domain=$2; shift ;;
      --server-ip) need_value "$@"; opt_server_ips+=("$2"); shift ;;
      --acme-email) need_value "$@"; opt_acme_email=$2; shift ;;
      --from-dir) need_value "$@"; opt_from_dir=$2; shift ;;
      --build-disk) need_value "$@"; opt_build_disk=$2; shift ;;
      --no-builds) opt_builds=off ;;
      --builds) opt_builds=on ;;
      --channel=* | --version=* | --panel-domain=* | --server-ip=* | --acme-email=* | --from-dir=* | --build-disk=*)
        set -- "${1%%=*}" "${1#*=}" "${@:2}"
        continue ;;
      -y | --yes) opt_yes=1 ;;
      --uninstall) opt_uninstall=1 ;;
      --purge) opt_purge=1 ;;
      -h | --help) usage; exit 0 ;;
      *) die "unknown option '$1' (see --help)" ;;
    esac
    shift
  done

  case $opt_channel in
    "" | stable | nightly) ;;
    *) die "--channel must be stable or nightly" ;;
  esac
  if [ -n "$opt_version" ]; then
    [[ $opt_version =~ ^[0-9] ]] && opt_version=v$opt_version
    [[ $opt_version =~ ^[A-Za-z0-9._-]+$ ]] || die "--version '$opt_version' is not a release name"
  fi
  if [ -n "$opt_panel_domain" ]; then
    opt_panel_domain=$(printf '%s' "$opt_panel_domain" | tr '[:upper:]' '[:lower:]')
    opt_panel_domain=${opt_panel_domain%.}
    valid_domain "$opt_panel_domain" || die "--panel-domain '$opt_panel_domain' is not a domain name"
  fi
  if [ -n "$opt_acme_email" ] && ! [[ $opt_acme_email =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]]; then
    die "--acme-email '$opt_acme_email' is not an e-mail address"
  fi
  [ "$opt_purge" = 0 ] || [ "$opt_uninstall" = 1 ] || die "--purge only works together with --uninstall"
  if [ -n "$opt_build_disk" ]; then
    [[ $opt_build_disk =~ ^[0-9]{1,5}[GT]$ ]] || die "--build-disk '$opt_build_disk' is not a size like 20G"
  fi
  return 0
}

need_value() { [ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value"; }

valid_domain() {
  [[ $1 =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]] && [ ${#1} -le 253 ]
}

# Questions go to the terminal, not to stdin: with "curl | bash" stdin is the
# script itself. Only --purge asks.
# Questions are only asked when the script reads from a terminal. When it is
# piped in (curl … | sudo bash) its input is the script itself, and reading
# /dev/tty instead hangs under sudo implementations that put the command on
# a new pseudo-terminal without passing keystrokes on (sudo-rs, use_pty).
interactive() { [ -t 0 ] && [ -t 1 ]; }

ask() { # ask <prompt> -> answer on stdout
  local answer=""
  printf '%s' "$1" >&2
  read -r answer || true
  printf '%s' "$answer"
}

confirm() { # confirm <question> <default y|n>
  local answer
  answer=$(ask "$1 [$([ "$2" = y ] && echo Y/n || echo y/N)] ")
  answer=${answer:-$2}
  [[ $answer =~ ^[Yy] ]]
}

# ---------------------------------------------------------------------------
# Flags in OWNPANELD_ARGS. Values never contain spaces (systemd splits the
# variable on whitespace), so the flags are handled as a list of words.

flag_values() { # flag_values <args> <name>: every value of -name, one per line
  local -a words
  local i
  read -ra words <<<"$1"
  for ((i = 0; i < ${#words[@]}; i++)); do
    case ${words[i]} in
      "-$2" | "--$2") [ $((i + 1)) -lt ${#words[@]} ] && printf '%s\n' "${words[i + 1]}"; i=$((i + 1)) ;;
      "-$2="* | "--$2="*) printf '%s\n' "${words[i]#*=}" ;;
    esac
  done
}

without_flag() { # without_flag <args> <name>: args without any -name
  local -a words out=()
  local i
  read -ra words <<<"$1"
  for ((i = 0; i < ${#words[@]}; i++)); do
    case ${words[i]} in
      "-$2" | "--$2") i=$((i + 1)) ;;
      "-$2="* | "--$2="*) ;;
      *) out+=("${words[i]}") ;;
    esac
  done
  printf '%s' "${out[*]}"
}

# ---------------------------------------------------------------------------

check_system() {
  [ -d /run/systemd/system ] || die "ownpanel needs systemd, and this system was not started with it"
  [ -r /etc/os-release ] || die "cannot tell which Linux this is (/etc/os-release is missing)"
  local id version major
  # shellcheck disable=SC1091
  id=$(. /etc/os-release && echo "${ID:-}")
  # shellcheck disable=SC1091
  version=$(. /etc/os-release && echo "${VERSION_ID:-}")
  major=${version%%.*}
  case $id in
    debian) [[ $major =~ ^[0-9]+$ ]] && [ "$major" -ge 12 ] || die "Debian $version is not supported; ownpanel needs Debian 12 or newer" ;;
    ubuntu) [[ $major =~ ^[0-9]+$ ]] && [ "$major" -ge 22 ] || die "Ubuntu $version is not supported; ownpanel needs Ubuntu 22.04 or newer" ;;
    *) die "$id $version is not supported; ownpanel installs on Debian 12+ and Ubuntu 22.04+" ;;
  esac
  os_name="$id $version"

  case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "processor type $(uname -m) is not supported; ownpanel runs on amd64 and arm64" ;;
  esac

  local cmd
  for cmd in tar sha256sum install useradd usermod groupadd getent systemctl setpriv; do
    command -v "$cmd" >/dev/null || die "the command '$cmd' is missing"
  done
  if [ -z "$opt_from_dir" ]; then
    command -v curl >/dev/null || die "curl is needed to download ownpanel: apt-get install -y curl"
  fi
}

# Reads what is installed now.
read_installed() {
  installed=0
  [ -f "$UNITS/ownpaneld.service" ] && installed=1
  old_channel=""
  old_version=""
  if [ -f "$RELEASE_FILE" ]; then
    old_channel=$(sed -n 's/^channel=//p' "$RELEASE_FILE" | tail -n 1)
    old_version=$(sed -n 's/^version=//p' "$RELEASE_FILE" | tail -n 1)
  fi
  if [ -z "$old_version" ] && [ -x "$BIN/ownpaneld" ]; then
    old_version=$("$BIN/ownpaneld" version 2>/dev/null) || old_version="unknown"
  fi
  old_args=""
  [ -f "$ENV_FILE" ] && old_args=$(sed -n 's/^OWNPANELD_ARGS=//p' "$ENV_FILE" | tail -n 1)
  return 0
}

# Fetches the release into a temporary directory, or uses --from-dir. Sets src
# and new_version.
get_release() {
  if [ -n "$opt_from_dir" ]; then
    [ -d "$opt_from_dir" ] || die "--from-dir: $opt_from_dir is not a directory"
    src=$(cd "$opt_from_dir" && pwd)
    step "Installing from $src"
  else
    lookup_release "$(release_tag)"
    fetch_release
  fi
  check_release "${rel_version:-}"
  if [ -n "$opt_from_dir" ]; then
    case $new_version in nightly-*) channel=nightly ;; *) channel=stable ;; esac
  fi
}

# release_tag: which release to look up: the --version, "nightly", or
# "latest" (the newest stable release).
release_tag() {
  if [ -n "$opt_version" ]; then
    case $opt_version in nightly*) echo nightly ;; *) echo "$opt_version" ;; esac
  elif [ "$channel" = nightly ]; then
    echo nightly
  else
    echo latest
  fi
}

# json_flat prints every value of a JSON document on its own line as
# "<path><TAB><value>", e.g. "assets[1].name<TAB>ownpanel_v1.2.3_linux_amd64.tar.gz".
# Strings stay escaped. Enough for GitHub's answers, without needing jq.
json_flat() {
  awk '
    { s = s $0 "\n" }
    END {
      n = length(s); i = 1; d = 0; ek = 0
      while (i <= n) {
        c = substr(s, i, 1)
        if (c == "{" || c == "[") {
          d++; t[d] = c; k[d] = ""; x[d] = 0; ek = (c == "{"); i++
        } else if (c == "}" || c == "]") {
          d--; i++
        } else if (c == ",") {
          if (t[d] == "[") x[d]++; else ek = 1
          i++
        } else if (c == ":") {
          ek = 0; i++
        } else if (c == "\"") {
          j = i + 1; v = ""
          while (j <= n) {
            c = substr(s, j, 1)
            if (c == "\\") { v = v substr(s, j, 2); j += 2; continue }
            if (c == "\"") break
            v = v c; j++
          }
          i = j + 1
          if (t[d] == "{" && ek) k[d] = v; else emit(v)
        } else if (c ~ /[ \t\r\n]/) {
          i++
        } else {
          j = i
          while (j <= n && substr(s, j, 1) !~ /[,}\] \t\r\n]/) j++
          emit(substr(s, i, j - i)); i = j
        }
      }
    }
    function emit(v,   p, e) {
      p = ""
      for (e = 1; e <= d; e++) p = p (t[e] == "{" ? (e > 1 ? "." : "") k[e] : "[" x[e] "]")
      print p "\t" v
    }'
}

# json_get <flat> <path>: the value at path in json_flat output.
json_get() { printf '%s\n' "$1" | awk -F '\t' -v p="$2" '$1 == p { print $2; exit }'; }

# asset_id <flat> <name>: the id of the release asset called name.
asset_id() {
  printf '%s\n' "$1" | awk -F '\t' -v want="$2" '
    { f = "" }
    match($1, /^assets\[[0-9]+\]\./) { i = substr($1, 8, RLENGTH - 9); f = substr($1, RLENGTH + 1) }
    f == "name" && $2 == want { hit[i] = 1 }
    f == "id" { id[i] = $2 }
    END { for (i in hit) if (id[i] ~ /^[0-9]+$/) print id[i] }' | head -n 1
}

# lookup_release <tag|latest>: asks GitHub's API about one release and sets
# rel_tag, rel_version (the build in it), rel_archive (the archive for this
# machine) and the ids of the archive and checksums.txt. Downloads then go
# by asset id, so a moving tag (nightly) can never mix two builds.
lookup_release() { try_lookup_release "$1" || die "$rel_error"; }

# try_lookup_release <tag|latest>: lookup_release that returns 1 with the
# reason in rel_error instead of stopping.
try_lookup_release() {
  local what=$1 path flat name
  rel_error=""
  if [ "$what" = latest ]; then path=releases/latest; else path=releases/tags/$what; fi
  if ! curl -fsSL --retry 3 --max-time 60 "${CURL_LIMITS[@]}" -H "Accept: application/vnd.github+json" \
    -o "$tmp/release.json" "$API/repos/$REPO/$path"; then
    if [ "$what" = latest ]; then
      rel_error="could not look up the latest release of $REPO (none published yet? try --channel nightly)"
    else
      rel_error="could not look up the release $what of $REPO (does it exist?)"
    fi
    return 1
  fi
  flat=$(json_flat <"$tmp/release.json")
  rel_tag=$(json_get "$flat" tag_name)
  if ! [[ $rel_tag =~ ^[A-Za-z0-9._-]+$ ]]; then
    rel_error="could not read the release $what of $REPO"
    return 1
  fi
  if [ "$rel_tag" = nightly ]; then
    # The nightly is one release whose title names the current build.
    name=$(json_get "$flat" name)
    rel_version=$(printf '%s\n' "$name" | grep -oE 'nightly-[0-9]{8}-[0-9a-f]+' | head -n 1 || true)
    if [ -z "$rel_version" ]; then
      rel_error="could not tell which build the nightly release holds (title: '$name')"
      return 1
    fi
    if [ -n "${opt_version:-}" ] && [ "$opt_version" != "$rel_version" ]; then
      die "$opt_version is no longer available; the nightly release now holds $rel_version"
    fi
  else
    rel_version=$rel_tag
  fi
  rel_archive=ownpanel_${rel_version}_linux_${arch}.tar.gz
  rel_archive_id=$(asset_id "$flat" "$rel_archive")
  rel_sums_id=$(asset_id "$flat" checksums.txt)
  if [ -z "$rel_archive_id" ] || [ -z "$rel_sums_id" ]; then
    rel_error="the release $rel_tag ($rel_version) has no $rel_archive and checksums.txt yet; it may be being published right now, try again in a few minutes"
    return 1
  fi
}

# download_asset <id> <file>: fails with curl's exit code (22 when GitHub
# answers with an error such as 404, 28 when the download stalls). curl runs
# in the background so that a signal to this script is handled right away
# instead of after the download (update.sh's trap stops curl too).
download_asset() {
  curl -fsSL --retry 2 --max-time 600 "${CURL_LIMITS[@]}" -H "Accept: application/octet-stream" \
    -o "$2" "$API/repos/$REPO/releases/assets/$1" &
  wait "$!"
}

# fetch_release: downloads and checks the release found by lookup_release
# and unpacks it. Sets src.
#
# Every build of main replaces the nightly in place: the title changes, the
# new archives and checksums.txt are uploaded and the old archives deleted.
# A download that started just before may find its files gone. Then, unless a
# --version was asked for, the nightly is looked up again and the build it
# holds now is downloaded instead (update.sh hears of it through
# release_moved, which it defines).
fetch_release() {
  local lookups=0
  until try_fetch_release; do
    [ "$rel_moved" = 1 ] && [ "$rel_tag" = nightly ] || die "$rel_error"
    # Asked for exactly this build: say that it is gone (the lookup stops
    # with that when the nightly holds another one by now).
    if [ -n "${opt_version:-}" ]; then
      lookup_release nightly
      die "$rel_error"
    fi
    warn "$rel_error; the nightly release is being replaced, looking it up again"
    while :; do
      lookups=$((lookups + 1))
      [ "$lookups" -le "$NIGHTLY_LOOKUPS" ] ||
        die "the nightly release is being replaced right now ($rel_error); try again in a few minutes"
      [ "$lookups" = 1 ] || sleep "$NIGHTLY_WAIT"
      try_lookup_release nightly && break
      warn "$rel_error"
    done
    say "The nightly release now holds $rel_version."
    if declare -F release_moved >/dev/null; then release_moved; fi
  done
  mkdir "$tmp/release"
  tar -xzf "$tmp/$rel_archive" -C "$tmp/release" --no-same-owner
  src=$tmp/release/${rel_archive%.tar.gz}
}

# try_fetch_release: downloads and verifies the archive of the release found
# by lookup_release. Returns 1 with the reason in rel_error; rel_moved is 1
# when the release may have changed meanwhile (a file is gone, or the files
# do not belong together).
try_fetch_release() {
  local rc=0
  rel_moved=0
  step "Downloading ownpanel $rel_version ($channel) for linux/$arch"
  download_asset "$rel_sums_id" "$tmp/checksums.txt" || rc=$?
  if [ "$rc" != 0 ]; then
    rel_error="could not download checksums.txt of $rel_tag"
    [ "$rc" != 22 ] || rel_moved=1
    return 1
  fi
  if ! grep -qE "^[0-9a-f]{64}  $rel_archive\$" "$tmp/checksums.txt"; then
    rel_error="checksums.txt of $rel_tag does not list $rel_archive; the release may be being published right now, try again in a few minutes"
    rel_moved=1
    return 1
  fi
  download_asset "$rel_archive_id" "$tmp/$rel_archive" || rc=$?
  if [ "$rc" != 0 ]; then
    rel_error="could not download $rel_archive"
    [ "$rc" != 22 ] || rel_moved=1
    return 1
  fi
  if ! (cd "$tmp" && grep -E "^[0-9a-f]{64}  $rel_archive\$" checksums.txt | sha256sum -c --quiet -); then
    rel_error="the checksum of $rel_archive does not match checksums.txt; nothing was installed"
    rel_moved=1
    return 1
  fi
  say "Checksum verified: $rel_archive"
}

# check_release [version]: src is a complete release for this machine (and
# holds that version). Sets new_version.
check_release() {
  local f want
  for f in "${RELEASE_FILES[@]}"; do
    [ -f "$src/$f" ] || die "$src is not an ownpanel release: $f is missing"
  done
  new_version=$(tr -d '[:space:]' <"$src/VERSION")
  if [ -n "${1:-}" ] && [ "$new_version" != "$1" ]; then
    die "the downloaded archive holds $new_version, not $1; nothing was installed"
  fi
  # The ELF machine field (byte 18): 3e is x86-64, b7 is AArch64. Checked
  # because a system with emulation would run the wrong build, slowly.
  want=$([ "$arch" = amd64 ] && echo 3e || echo b7)
  for f in ownpaneld caddy; do
    [ "$(od -An -tx1 -j18 -N1 "$src/$f" | tr -d ' ')" = "$want" ] ||
      die "$src/$f is not built for linux/$arch"
  done
  "$src/ownpaneld" version >/dev/null 2>&1 || die "$src/ownpaneld does not run on this machine"
}

# is_public_ip <addr>: roughly the rule ownpaneld applies to -server-ip. Only
# used to choose the address the final message shows; the daemon decides.
is_public_ip() {
  local a=$1 o1 o2
  case $a in
    *:*)
      a=$(printf '%s' "$a" | tr '[:upper:]' '[:lower:]')
      [[ $a =~ ^[23][0-9a-f]{0,3}: ]] || return 1     # global unicast 2000::/3
      [[ $a =~ ^2001:0?db8: ]] && return 1             # documentation
      return 0 ;;
    *.*.*.*)
      IFS=. read -r o1 o2 _ <<<"$a"
      case $o1 in
        0 | 10 | 127) return 1 ;;
        100) [ "$o2" -ge 64 ] && [ "$o2" -le 127 ] && return 1 ;;
        169) [ "$o2" = 254 ] && return 1 ;;
        172) [ "$o2" -ge 16 ] && [ "$o2" -le 31 ] && return 1 ;;
        192) [ "$o2" = 168 ] && return 1; [[ $a =~ ^192\.0\.[02]\. ]] && return 1 ;;
        198) [[ $o2 =~ ^1[89]$ ]] && return 1; [[ $a =~ ^198\.51\.100\. ]] && return 1 ;;
        203) [[ $a =~ ^203\.0\.113\. ]] && return 1 ;;
      esac
      [ "$o1" -lt 224 ] ;;
    *) return 1 ;;
  esac
}

# Public addresses this server uses to reach the Internet, according to the
# routing table: IPv4 first, then IPv6.
detect_public_ips() {
  command -v ip >/dev/null || return 0
  local addr
  for addr in \
    "$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n 1)" \
    "$(ip -6 route get 2606:4700:4700::1111 2>/dev/null | sed -n 's/.* src \([0-9a-fA-F:]*\).*/\1/p' | head -n 1)"; do
    if [ -n "$addr" ] && is_public_ip "$addr"; then printf '%s\n' "$addr"; fi
  done
}

# Works out the new OWNPANELD_ARGS from the stored ones and the options.
# Domain, addresses and e-mail are optional: without them the person sets
# them in the panel's first-run setup. Sets new_args.
choose_settings() {
  local base
  if [ -z "$old_args" ] || [ "$old_args" = "-tls acme" ]; then
    # Fresh, or the default of the first installer (Caddy admin API on TCP).
    base=$DEFAULT_ARGS
  else
    base=$old_args
  fi

  local ips=() v parts
  for v in "${opt_server_ips[@]}"; do
    IFS=, read -ra parts <<<"$v"
    ips+=("${parts[@]}")
  done
  for v in "${ips[@]}"; do
    [[ $v =~ ^[0-9a-fA-F:.]+$ ]] || die "--server-ip '$v' is not an IPv4 or IPv6 address"
  done

  new_args=$base
  # Installs from before the mail service do not pass its socket yet.
  if [ -z "$(flag_values "$new_args" mail-admin)" ]; then
    new_args="$new_args $MAIL_ADMIN_ARG"
  fi
  # Nor those from before the mail host's account pages its web socket.
  if [ -z "$(flag_values "$new_args" mail-web)" ]; then
    new_args="$new_args $MAIL_WEB_ARG"
  fi
  # Nor those from before builds the build service's; the panel asks it
  # whether this server can build (and says why not when it does not answer).
  if [ -z "$(flag_values "$new_args" build-worker)" ]; then
    new_args="$new_args $BUILD_WORKER_ARG"
  fi
  if [ ${#ips[@]} -gt 0 ]; then
    new_args="$(without_flag "$new_args" server-ip) -server-ip $(IFS=,; echo "${ips[*]}")"
  fi
  if [ -n "$opt_panel_domain" ]; then
    new_args="$(without_flag "$new_args" panel-domain) -panel-domain $opt_panel_domain"
  fi
  if [ -n "$opt_acme_email" ]; then
    new_args="$(without_flag "$new_args" acme-email) -acme-email $opt_acme_email"
  fi
  return 0
}

env_content() {
  cat <<EOF
# Flags for ownpaneld; see the Configuration page of the ownpanel wiki.
# install.sh keeps this file on upgrades and only replaces the flags you give
# it as options (--panel-domain, --server-ip, --acme-email).
# Apply a change by hand with: systemctl restart ownpaneld
OWNPANELD_ARGS=$new_args
EOF
}

release_content() {
  cat <<EOF
# Written by install.sh: the installed release and the channel upgrades follow.
channel=$channel
version=$new_version
EOF
}

# Something other than our Caddy on port 80 or 443 stops sites from working;
# another mail server on the mail ports stops ownpanel's mail service. Only a
# warning: the panel shows a busy port under Mail health, and updates must
# never stop because of it (many servers run a local mailer for cron).
check_ports() {
  command -v ss >/dev/null || return 0
  local busy
  busy=$(ss -Hltnp '( sport = :80 or sport = :443 )' 2>/dev/null | grep -v '"caddy"' || true)
  if [ -n "$busy" ]; then
    warn "another program listens on port 80 or 443; Caddy cannot serve your sites until it is stopped:"
    printf '%s\n' "$busy" >&2
  fi
  busy=$(ss -Hltnp '( sport = :25 or sport = :465 or sport = :587 or sport = :993 or sport = :4190 )' 2>/dev/null | grep -v '"ownpaneld"' || true)
  if [ -n "$busy" ]; then
    warn "another program listens on a mail port (25, 465, 587, 993 or 4190); ownpanel's e-mail cannot use it until that program is stopped (e.g. systemctl disable --now exim4 or postfix):"
    printf '%s\n' "$busy" >&2
  fi
  if [ "$installed" = 0 ] && [ -e "$BIN/caddy" ]; then
    warn "$BIN/caddy exists and will be replaced by the Caddy that comes with ownpanel"
  fi
}

# Lists what an install would change, in "changes".
plan_changes() {
  changes=()
  local f
  if [ "$installed" = 0 ]; then
    changes+=("new install of ownpanel $new_version ($channel)")
  else
    [ "$old_version" != "$new_version" ] && changes+=("ownpanel ${old_version:-unknown} -> $new_version")
    [ -n "$old_channel" ] && [ "$old_channel" != "$channel" ] && changes+=("channel $old_channel -> $channel")
    cmp -s "$src/ownpaneld" "$BIN/ownpaneld" || [ "$old_version" != "$new_version" ] || changes+=("ownpaneld binary replaced")
    cmp -s "$src/caddy" "$BIN/caddy" || changes+=("Caddy $(caddy_version "$BIN/caddy") -> $(caddy_version "$src/caddy")")
    for f in ownpaneld.service ownpanel-caddy.service ownpanel-mail.service "${UPDATE_UNITS[@]}"; do
      if [ ! -f "$UNITS/$f" ]; then
        changes+=("unit file $f added")
      else
        cmp -s "$src/deploy/$f" "$UNITS/$f" || changes+=("unit file $f updated")
      fi
    done
    for f in install.sh update.sh deploy/ownpanel-update; do
      cmp -s "$src/$f" "$LIB/${f#deploy/}" || changes+=("$LIB/${f#deploy/} updated")
    done
    cmp -s "$src/deploy/caddy-bootstrap.json" "$ETC/caddy-bootstrap.json" || changes+=("Caddy bootstrap file updated")
    [ "$old_args" != "$new_args" ] && changes+=("settings: OWNPANELD_ARGS=$old_args" "       -> OWNPANELD_ARGS=$new_args")
    for f in "${BUILD_UNITS[@]}"; do
      [ -f "$UNITS/$f" ] && ! cmp -s "$src/deploy/$f" "$UNITS/$f" && changes+=("unit file $f updated")
    done
  fi
  if [ "$builds_ok" = 1 ]; then
    if [ ! -f "$UNITS/ownpanel-build.service" ]; then
      changes+=("set up building sites on this server (installs podman and reserves disk space)")
    elif ! grep -q '^state=ready$' "$BUILD_STATE" 2>/dev/null; then
      changes+=("building sites on this server: set up again")
    elif [ -n "$opt_build_disk" ]; then
      changes+=("disk space for builds: $opt_build_disk")
    fi
  elif [ "$builds_reason" = off ]; then
    [ -f "$UNITS/ownpanel-build.service" ] && changes+=("building sites on this server: turned off")
  else
    changes+=("building sites on this server: not possible here ($builds_detail)")
  fi
  return 0
}

caddy_version() { "$1" version 2>/dev/null | awk '{print $1}' || echo unknown; }

install_files() {
  step "Installing"
  # Users. ownpaneld reaches Caddy's admin socket through the group caddy
  # (see ownpanel-caddy.service); Caddy's own directories stay 0700.
  id -u caddy >/dev/null 2>&1 || useradd --system --home "$CADDY_DATA/home" --shell /usr/sbin/nologin caddy
  id -u ownpanel >/dev/null 2>&1 || useradd --system --home "$DATA" --shell /usr/sbin/nologin ownpanel
  usermod -a -G caddy ownpanel
  # The mail service runs as its own user; ownpaneld reaches its admin socket
  # through the group ownpanel-mail (see ownpanel-mail.service). The mail data
  # in $MAIL_DATA stays 0700, so the membership grants nothing else.
  id -u ownpanel-mail >/dev/null 2>&1 || useradd --system --user-group --home "$MAIL_DATA" --shell /usr/sbin/nologin ownpanel-mail
  usermod -a -G ownpanel-mail ownpanel
  # Caddy reaches the mail service's web socket (not its admin socket)
  # through the group ownpanel-mail-web.
  getent group "$MAIL_WEB_GROUP" >/dev/null || groupadd --system "$MAIL_WEB_GROUP"
  usermod -a -G "$MAIL_WEB_GROUP" caddy
  # Builds run as their own user; ownpaneld reaches the build service's
  # socket through the group ownpanel-build (see ownpanel-build.service).
  # Made here, before ownpaneld starts, so the membership applies to it.
  if [ "$builds_ok" = 1 ]; then
    id -u "$BUILD_USER" >/dev/null 2>&1 ||
      useradd --system --user-group --home-dir "$BUILD_HOME" --no-create-home --shell /usr/sbin/nologin "$BUILD_USER"
    usermod -a -G "$BUILD_USER" ownpanel
  fi

  [ "$installed" = 1 ] && systemctl stop ownpaneld ownpanel-mail ownpanel-caddy 2>/dev/null || true

  install -m 0755 "$src/ownpaneld" "$src/caddy" "$BIN/"
  install -m 0644 "$src/deploy/ownpaneld.service" "$src/deploy/ownpanel-caddy.service" "$src/deploy/ownpanel-mail.service" "$UNITS/"
  # The updater: update.sh, the installer it loads, and the root-side worker
  # that carries out update requests from the panel. install replaces files
  # by unlinking them first, so an update.sh running this installer is not
  # affected.
  install -d -m 0755 "$LIB"
  install -m 0755 "$src/install.sh" "$src/update.sh" "$src/deploy/ownpanel-update" "$LIB/"
  local u
  for u in "${UPDATE_UNITS[@]}"; do
    install -m 0644 "$src/deploy/$u" "$UNITS/"
  done

  install -d -m 0755 -o ownpanel -g ownpanel "$DATA"
  # Caddy's directory: Caddy creates storage and home in it itself.
  install -d -m 0700 -o caddy -g caddy "$CADDY_DATA"
  # The panel (group ownpanel) may create files in requests: an update
  # request and the automatic update setting. The sticky bit keeps it from
  # replacing or deleting the status file root writes there.
  install -d -m 0755 -o root -g root "$UPDATE_ROOT"
  install -d -m 1770 -o root -g ownpanel "$UPDATE_DIR"
  install -d -m 0700 -o root -g root "$BACKUP_DIR"
  migrate_layout
  install -d -m 0755 "$ETC"
  install -m 0644 "$src/deploy/caddy-bootstrap.json" "$ETC/caddy-bootstrap.json"
  # The files hold flags and the version, no secrets.
  env_content >"$ENV_FILE.new" && chmod 0644 "$ENV_FILE.new" && mv "$ENV_FILE.new" "$ENV_FILE"
  release_content >"$RELEASE_FILE.new" && chmod 0644 "$RELEASE_FILE.new" && mv "$RELEASE_FILE.new" "$RELEASE_FILE"

  # A config saved by an early version enabled the admin API on TCP and would
  # bring it back on --resume; drop it, ownpaneld pushes the full config again.
  # Done as caddy: the directory is Caddy's.
  # shellcheck disable=SC2016 # expanded by that sh
  setpriv --reuid caddy --regid caddy --clear-groups --reset-env sh -c \
    'f=$1; if [ -f "$f" ] && ! grep -q "unix//" "$f"; then rm -f "$f"; fi' sh "$CADDY_DATA/home/caddy/autosave.json" || true

  systemctl daemon-reload
  systemctl enable --now "${SERVICES[@]}" 2>&1 | grep -v '^Created symlink' || true
  systemctl enable --now ownpanel-update.path ownpanel-update.timer 2>&1 | grep -v '^Created symlink' || true
}

# migrate_layout moves what older versions kept inside $DATA (Caddy's
# directories, the update requests and the backups) to the directories above.
# The services are stopped, so no process of the panel's user can change
# $DATA meanwhile. Each directory is first renamed into a root-only staging
# directory (a rename never follows a link) and checked there: only a real
# directory with the owner ownpanel gave it is used. The old names become
# links to the new places, so the previous version still finds everything if
# an update is rolled back; nothing run as root uses those links.
migrate_layout() {
  local stage=$UPDATE_ROOT/.migrating
  rm -rf -- "$stage"
  install -d -m 0700 -o root -g root "$stage"
  migrate_dir "$DATA/caddy" "$CADDY_DATA/storage" caddy
  migrate_dir "$DATA/caddy-home" "$CADDY_DATA/home" caddy
  migrate_dir "$DATA/update" "$UPDATE_DIR" root
  migrate_dir "$DATA/backups" "$BACKUP_DIR" root
  rm -rf -- "$stage"
  # The caddy user created for ownpanel had its home in $DATA.
  if [ "$(getent passwd caddy | cut -d: -f6)" = "$DATA/caddy-home" ]; then
    usermod -d "$CADDY_DATA/home" caddy
  fi
}

# migrate_dir <old> <new> <owner>: moves the directory old to new and leaves
# a link to new at old. Caddy's directories are moved as a whole (new must
# not exist yet); for the updater's, which exist already, the files in them
# are moved.
migrate_dir() {
  local old=$1 new=$2 owner=$3 staged
  # Missing, or a link: nothing to move (a link is never followed).
  [ -d "$old" ] && [ ! -L "$old" ] || return 0
  if [ "$owner" = caddy ] && { [ -e "$new" ] || [ -L "$new" ]; }; then
    warn "$old was not moved: $new exists already"
    return 0
  fi
  staged=$UPDATE_ROOT/.migrating/$(basename "$new")
  if ! mv -T -- "$old" "$staged"; then
    warn "$old could not be moved to $new"
    return 0
  fi
  if [ -L "$staged" ] || [ ! -d "$staged" ] || [ "$(stat -c %U "$staged")" != "$owner" ]; then
    warn "$old was not the directory ownpanel created (owned by $(stat -c %U "$staged")); it was removed"
    rm -rf -- "$staged"
    return 0
  fi
  if [ "$owner" = caddy ]; then
    mv -T -- "$staged" "$new"
  else
    find "$staged" -mindepth 1 -maxdepth 1 -type f -exec mv -n -t "$new" -- {} +
    rm -rf -- "$staged"
  fi
  ln -sT "$new" "$old" 2>/dev/null || true
  say "Moved $old to $new"
}

open_firewall() {
  if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active'; then
    local p
    for p in 80 443 25 465 587 993 4190; do ufw allow "$p/tcp" >/dev/null; done
    say "Firewall (ufw): allowed 80, 443 (web) and 25, 465, 587, 993, 4190 (e-mail) over TCP."
  else
    say "Firewall: make sure 80, 443 (web) and 25, 465, 587, 993, 4190 (e-mail) are open over TCP"
    say "to the Internet (no ufw active here). Many hosting providers block port 25 until"
    say "you ask them to unblock it; see the E-Mail page of the ownpanel wiki."
  fi
  say "Do not open port 8600; the panel only listens on this machine."
}

# listen_addr: where ownpaneld answers, from -listen (default 127.0.0.1:8600).
listen_addr() {
  local l
  l=$(flag_values "$new_args" listen | tail -n 1)
  l=${l:-127.0.0.1:8600}
  case $l in
    :*) l=127.0.0.1$l ;;
    0.0.0.0:*) l=127.0.0.1:${l##*:} ;;
    "[::]:"*) l="[::1]:${l##*:}" ;;
  esac
  printf '%s' "$l"
}

# panel_ready <seconds>: whether the panel answers within that time.
panel_ready() {
  local addr i
  addr=$(listen_addr)
  for ((i = 0; i < $1; i++)); do
    if curl -fsS -o /dev/null --max-time 2 "http://$addr/api/session" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  return 1
}

wait_ready() {
  local addr
  addr=$(listen_addr)
  panel_ready 60 && return 0
  systemctl --no-pager --lines=0 status "${SERVICES[@]}" >&2 || true
  journalctl --no-pager -u ownpaneld -n 20 >&2 || true
  die "the panel did not answer on http://$addr within 60 seconds (see the log above: journalctl -u ownpaneld)"
}

print_result() {
  local addr port domain ips=() v ip url_ip=""
  addr=$(listen_addr)
  port=${addr##*:}
  domain=$(flag_values "$new_args" panel-domain | tail -n 1)
  while IFS= read -r v; do
    IFS=, read -ra parts <<<"$v"
    ips+=("${parts[@]}")
  done < <(flag_values "$new_args" server-ip)
  [ ${#ips[@]} -gt 0 ] || mapfile -t ips < <(detect_public_ips)
  for ip in "${ips[@]}"; do
    if is_public_ip "$ip"; then
      url_ip=$ip
      break
    fi
  done
  case $url_ip in *:*) url_ip="[$url_ip]" ;; esac

  step "ownpanel $new_version ($channel channel) is running"
  if [ -n "$domain" ]; then
    say "Open the panel:  https://$domain"
    say
    say "DNS: the panel domain must point at this server:"
    for ip in "${ips[@]}"; do
      case $ip in *:*) say "  $domain.  AAAA  $ip" ;; *) say "  $domain.  A     $ip" ;; esac
    done
    [ ${#ips[@]} -gt 0 ] || say "  $domain.  A/AAAA  <public address of this server>"
    say "The certificate is requested automatically as soon as the domain points here."
  elif [ ! -f "$SETUP_CODE" ]; then
    # Set up already: the panel's domain is most likely set in the panel, which
    # this script cannot see. Do not claim it has none (issue #67).
    say "Open the panel at its usual address."
    if [ -n "$url_ip" ]; then
      say "https://$url_ip/ works too: it leads to the panel's domain once one is set in"
      say "the panel."
    fi
  elif [ -n "$url_ip" ]; then
    say "Open the panel:  https://$url_ip/"
    say "The panel has no domain yet, so it uses a self-signed certificate: accept the"
    say "browser's warning once."
    [ -f "$SETUP_CODE" ] && say "Then follow the setup steps; they ask for the panel's domain, the server" &&
      say "addresses and the certificate e-mail."
  else
    say "No public address was found (the server may be behind NAT), so the panel is"
    say "reachable through an SSH tunnel. On your computer run and keep open:"
    say "  ssh -L $port:$addr root@<server address>"
    say "then open http://127.0.0.1:$port"
    say "If the server has a public address, https://<that address>/ works too."
  fi
  say
  if [ -f "$SETUP_CODE" ]; then
    say "Setup code: $(tr -d '[:space:]' <"$SETUP_CODE")"
    say "Enter it in the panel to create the first administrator. It is only valid until"
    say "that administrator exists. Show it again with: sudo cat $SETUP_CODE"
  else
    say "An administrator already exists; sign in as usual."
  fi
}

# ---------------------------------------------------------------------------
# Builds. Sites are built on the server by code nobody reviewed, so builds
# run in a container per build (rootless Podman) as the user ownpanel-build,
# never as the panel's user or root. Root's part is done here and by
# ownpanel-build-setup.service (the build disk with its quotas and the
# packet filter; see deploy/ownpanel-build-setup). A server that cannot run
# builds still gets the panel: builds are then reported as unavailable, with
# the reason in $BUILD_STATE, which the panel reads.

# plan_builds decides whether builds are set up. Sets builds_ok (1 or 0) and,
# when not, builds_reason (a code the panel knows) and builds_detail.
plan_builds() {
  builds_ok=0
  builds_reason=""
  builds_detail=""
  local want=on id version major n
  if [ -n "$opt_builds" ]; then
    want=$opt_builds
  elif [ -f "$BUILD_ENV" ] && grep -q '^OWNPANEL_BUILDS=off$' "$BUILD_ENV"; then
    want=off
  fi
  if [ "$want" = off ]; then
    builds_reason=off
    builds_detail="turned off with install.sh --no-builds (install.sh --builds turns it on)"
    return 0
  fi
  # shellcheck disable=SC1091
  id=$(. /etc/os-release && echo "${ID:-}")
  # shellcheck disable=SC1091
  version=$(. /etc/os-release && echo "${VERSION_ID:-}")
  major=${version%%.*}
  # The Podman each release ships: Debian 12 4.3, Debian 13 5.4, Ubuntu
  # 22.04 3.4 (too old), Ubuntu 24.04 4.9.
  if [ "$id" = ubuntu ] && [ "$major" -lt 24 ]; then
    builds_reason=system
    builds_detail="Ubuntu $version ships Podman 3.4; builds need Podman 4.3 or newer (Ubuntu 24.04 or newer, or Debian 12 or newer)"
    return 0
  fi
  if [ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" != cgroup2fs ]; then
    builds_reason=system
    builds_detail="the system does not use control groups version 2, which build limits need"
    return 0
  fi
  n=$(cat /proc/sys/user/max_user_namespaces 2>/dev/null || echo 0)
  if ! [[ $n =~ ^[0-9]+$ ]] || [ "$n" -lt 1000 ] ||
    [ "$(cat /proc/sys/kernel/unprivileged_userns_clone 2>/dev/null || echo 1)" != 1 ]; then
    builds_reason=system
    builds_detail="user namespaces are turned off in this kernel (user.max_user_namespaces / kernel.unprivileged_userns_clone)"
    return 0
  fi
  if [ ! -e /dev/loop-control ] && ! modprobe loop 2>/dev/null; then
    builds_reason=disk
    builds_detail="loop devices are not available (is this server itself a container?)"
    return 0
  fi
  builds_ok=1
}

# write_build_state <ready|unavailable> [reason] [detail]
write_build_state() {
  install -d -m 0755 "$ETC"
  {
    echo "# Written by install.sh: whether building sites was set up on this server."
    echo "state=$1"
    echo "reason=${2:-}"
    echo "detail=$(printf '%s' "${3:-}" | tr -d '\n\r' | cut -c1-300)"
  } >"$BUILD_STATE.new"
  chmod 0644 "$BUILD_STATE.new"
  mv -f "$BUILD_STATE.new" "$BUILD_STATE"
}

# builds_unavailable <reason> <detail>: the build service is stopped and the
# reason recorded. Never stops the install.
builds_unavailable() {
  warn "building sites is not available on this server: $2"
  if [ -f "$UNITS/ownpanel-build.service" ]; then
    systemctl disable --now ownpanel-build.service 2>&1 | grep -v '^Removed' || true
  fi
  write_build_state unavailable "$1" "$2"
}

# run_apt <args>: apt-get outside of this process's sandbox. An update from
# the panel runs this script inside ownpanel-update.service, whose hardening
# (RestrictSUIDSGID, PrivateDevices, ...) would break installing packages
# with set-user-ID programs (newuidmap); systemd-run starts apt-get as a unit
# of its own. Questions are never asked.
run_apt() {
  local opts=(-o DPkg::Lock::Timeout=300 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold)
  if command -v systemd-run >/dev/null && [ -d /run/systemd/system ]; then
    systemd-run --quiet --wait --pipe --collect --service-type=exec -p TimeoutStartSec=1200 \
      --setenv=DEBIAN_FRONTEND=noninteractive -- apt-get "${opts[@]}" "$@" </dev/null
  else
    DEBIAN_FRONTEND=noninteractive apt-get "${opts[@]}" "$@" </dev/null
  fi
}

# install_build_packages: the distribution's packages builds need.
install_build_packages() {
  local missing=() p
  for p in "${BUILD_PACKAGES[@]}"; do
    dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'install ok installed' || missing+=("$p")
  done
  [ ${#missing[@]} -gt 0 ] || return 0
  say "Installing ${missing[*]} from the distribution's packages"
  if ! run_apt install -y -q --no-install-recommends "${missing[@]}" >/dev/null; then
    run_apt update -q >/dev/null || return 1
    run_apt install -y -q --no-install-recommends "${missing[@]}" >/dev/null || return 1
  fi
}

# podman_version_ok: the installed Podman is 4.3 or newer.
podman_version_ok() {
  local v major minor
  v=$(podman --version 2>/dev/null | awk '{print $NF}')
  IFS=. read -r major minor _ <<<"$v"
  podman_version=$v
  [[ $major =~ ^[0-9]+$ && $minor =~ ^[0-9]+$ ]] || return 1
  [ "$major" -gt 4 ] || { [ "$major" = 4 ] && [ "$minor" -ge 3 ]; }
}

# ensure_subids: a range of 65536 subordinate user and group ids for the
# build user (the users inside its containers), after every range in use.
ensure_subids() {
  local start
  if grep -q "^$BUILD_USER:" /etc/subuid 2>/dev/null && grep -q "^$BUILD_USER:" /etc/subgid 2>/dev/null; then
    return 0
  fi
  touch /etc/subuid /etc/subgid
  start=$(awk -F: '$2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ { e = $2 + $3; if (e > m) m = e } END { print m + 0 }' /etc/subuid /etc/subgid)
  [ "$start" -ge 524288 ] || start=524288
  start=$(((start + 65535) / 65536 * 65536))
  grep -q "^$BUILD_USER:" /etc/subuid || usermod --add-subuids "$start-$((start + 65535))" "$BUILD_USER"
  grep -q "^$BUILD_USER:" /etc/subgid || usermod --add-subgids "$start-$((start + 65535))" "$BUILD_USER"
}

# build_defaults: the settings of a new build environment, from the disk and
# memory of this server. Sets bd_* or returns 1 with builds_detail set.
build_defaults() {
  local total_kb avail_kb mem_kb disk_g max_g ws_g
  total_kb=$(df -Pk "$BUILD_BASE" | awk 'NR == 2 { print $2 }')
  avail_kb=$(df -Pk "$BUILD_BASE" | awk 'NR == 2 { print $4 }')
  mem_kb=$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo)
  if [ -n "$opt_build_disk" ]; then
    case $opt_build_disk in
      *T) disk_g=$((${opt_build_disk%T} * 1024)) ;;
      *) disk_g=${opt_build_disk%G} ;;
    esac
  else
    disk_g=$((total_kb * 15 / 100 / 1024 / 1024))
    [ "$disk_g" -ge 8 ] || disk_g=8
    [ "$disk_g" -le 32 ] || disk_g=32
  fi
  # The rest of the disk stays for everything else, with 4 GB to spare.
  max_g=$((avail_kb / 1024 / 1024 - 4))
  if [ -z "$opt_build_disk" ] && [ "$disk_g" -gt "$max_g" ]; then disk_g=$max_g; fi
  if [ "$disk_g" -lt 8 ] || [ "$disk_g" -gt "$max_g" ]; then
    builds_detail="not enough free disk space: builds need ${disk_g} GB (at least 8 GB) and 4 GB more must stay free; $((avail_kb / 1024 / 1024)) GB are free"
    return 1
  fi
  bd_slots=1
  if [ "$mem_kb" -ge $((6 * 1024 * 1024)) ]; then bd_slots=2; fi
  ws_g=4
  # Images 3 GB, waiting sources 1 GB, results 1 GB, a cache 1 GB.
  while [ $((bd_slots * ws_g + 6)) -gt "$disk_g" ]; do
    if [ "$bd_slots" -gt 1 ]; then bd_slots=$((bd_slots - 1)); elif [ "$ws_g" -gt 2 ]; then ws_g=2; else break; fi
  done
  bd_disk=${disk_g}G
  bd_workspace=${ws_g}G
}

build_env_content() {
  cat <<EOF
# Settings of the build environment; see the Configuration page of the
# ownpanel wiki. install.sh keeps this file on upgrades. Apply a change with:
#   systemctl restart ownpanel-build-setup ownpanel-build
# Changing OWNPANEL_BUILD_DISK_SIZE makes a new build disk (the caches go).
OWNPANEL_BUILDS=on
OWNPANEL_BUILD_DISK_SIZE=$bd_disk
OWNPANEL_BUILD_SLOTS=$bd_slots
OWNPANEL_BUILD_WORKSPACE_SIZE=$bd_workspace
OWNPANEL_BUILD_CACHE_SIZE=1G
OWNPANEL_BUILD_CACHES=100
OWNPANEL_BUILD_IMAGES_SIZE=3G
OWNPANEL_BUILD_SOURCES_SIZE=1G
OWNPANEL_BUILD_RESULTS_SIZE=1G
EOF
}

# set_build_env_value <key> <value>: in $BUILD_ENV, replaced or added.
set_build_env_value() {
  if grep -q "^$1=" "$BUILD_ENV"; then
    sed -i "s|^$1=.*|$1=$2|" "$BUILD_ENV"
  else
    printf '%s=%s\n' "$1" "$2" >>"$BUILD_ENV"
  fi
}

# setup_builds: packages, user, settings, units; then the build disk and
# the service start. Every failure ends in builds_unavailable, never in die.
setup_builds() {
  if [ "$builds_ok" != 1 ]; then
    if [ "$builds_reason" = off ] && [ -f "$BUILD_ENV" ]; then
      set_build_env_value OWNPANEL_BUILDS off
    elif [ "$builds_reason" = off ]; then
      install -d -m 0755 "$ETC"
      printf '%s\n' "# Building sites is turned off (install.sh --builds turns it on)." "OWNPANEL_BUILDS=off" >"$BUILD_ENV"
    fi
    builds_unavailable "$builds_reason" "$builds_detail"
    if [ -f "$UNITS/ownpanel-build-setup.service" ]; then
      systemctl disable --now ownpanel-build-setup.service 2>&1 | grep -v '^Removed' || true
    fi
    return 0
  fi
  step "Setting up builds"
  if ! install_build_packages; then
    builds_unavailable install "the packages for builds (${BUILD_PACKAGES[*]}) could not be installed; see the output above"
    return 0
  fi
  if ! podman_version_ok; then
    builds_unavailable runtime "Podman ${podman_version:-is missing}; builds need 4.3 or newer"
    return 0
  fi
  ensure_subids
  install -d -m 0755 -o root -g root "$BUILD_BASE"
  install -d -m 0700 -o "$BUILD_USER" -g "$BUILD_USER" "$BUILD_HOME"
  if [ "$(getent passwd "$BUILD_USER" | cut -d: -f6)" != "$BUILD_HOME" ]; then
    usermod -d "$BUILD_HOME" "$BUILD_USER"
  fi
  if [ ! -f "$BUILD_ENV" ] || ! grep -q '^OWNPANEL_BUILD_DISK_SIZE=' "$BUILD_ENV"; then
    if ! build_defaults; then
      builds_unavailable disk "$builds_detail"
      return 0
    fi
    build_env_content >"$BUILD_ENV.new" && chmod 0644 "$BUILD_ENV.new" && mv -f "$BUILD_ENV.new" "$BUILD_ENV"
  else
    set_build_env_value OWNPANEL_BUILDS on
    if [ -n "$opt_build_disk" ]; then set_build_env_value OWNPANEL_BUILD_DISK_SIZE "$opt_build_disk"; fi
  fi

  install -m 0755 "$src/deploy/ownpanel-build-setup" "$LIB/"
  local u why
  for u in "${BUILD_UNITS[@]}"; do
    install -m 0644 "$src/deploy/$u" "$UNITS/"
  done
  install -d -m 0755 "$NFT_DROPIN_DIR"
  install -m 0644 "$src/deploy/nftables-ownpanel-build.conf" "$NFT_DROPIN"
  systemctl daemon-reload
  systemctl enable "${BUILD_UNITS[@]}" 2>&1 | grep -v '^Created symlink' || true
  # Restarting the setup stops the build service (it is bound to it); both
  # start again with the new binary and settings.
  if ! systemctl restart ownpanel-build-setup.service; then
    why=$(journalctl --no-pager -o cat -u ownpanel-build-setup.service -n 20 2>/dev/null | grep 'ownpanel-build-setup:' | tail -n 1 | sed 's/^ownpanel-build-setup: //')
    case $why in
      *"free space"* | *"loop"* | *"build disk"* | *"fallocate"* | *DISK_SIZE*) builds_unavailable disk "${why:-the build disk could not be set up}" ;;
      *"packet filter"* | *nft*) builds_unavailable network "${why:-the packet filter for builds could not be set up}" ;;
      *) builds_unavailable install "${why:-ownpanel-build-setup failed (journalctl -u ownpanel-build-setup)}" ;;
    esac
    return 0
  fi
  if ! systemctl restart ownpanel-build.service; then
    builds_unavailable install "the build service did not start (journalctl -u ownpanel-build)"
    return 0
  fi
  write_build_state ready
  say "Builds: up to $(sed -n 's/^OWNPANEL_BUILD_SLOTS=//p' "$BUILD_ENV") at a time, $(sed -n 's/^OWNPANEL_BUILD_DISK_SIZE=//p' "$BUILD_ENV") of disk reserved (Podman $podman_version)."
  say "The build images are fetched in the background; the panel shows when builds are ready."
}

# remove_builds: the build services, their units and the build disk (it only
# holds caches and images). The user and the settings go with --purge.
remove_builds() {
  local s
  for s in ownpanel-build.service ownpanel-build-setup.service; do
    if [ -f "$UNITS/$s" ]; then
      systemctl disable --now "$s" 2>&1 | grep -v '^Removed' || true
      rm -f -- "${UNITS:?}/${s:?}"
      say "Removed $s"
    fi
  done
  if [ -f "$NFT_DROPIN" ]; then
    rm -f -- "${NFT_DROPIN:?}"
    rmdir "$NFT_DROPIN_DIR" 2>/dev/null || true
  fi
  if command -v nft >/dev/null; then nft delete table inet ownpanel_build 2>/dev/null || true; fi
  if mountpoint -q "$BUILD_BASE/work" 2>/dev/null; then umount "$BUILD_BASE/work" || umount -l "$BUILD_BASE/work" || true; fi
  if [ -e "$BUILD_BASE/disk.img" ]; then
    rm -f -- "${BUILD_BASE:?}/disk.img"
    say "Removed the build disk $BUILD_BASE/disk.img (it only held build caches and images)"
  fi
}

do_install() {
  check_system
  read_installed
  channel=${opt_channel:-${old_channel:-stable}}
  if [ -n "$opt_version" ]; then
    case $opt_version in nightly*) channel=nightly ;; *) channel=stable ;; esac
  fi

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  get_release
  choose_settings
  plan_builds
  plan_changes

  if [ "$installed" = 1 ] && [ "${#changes[@]}" -eq 0 ]; then
    step "ownpanel $new_version is already installed with these settings; nothing to change"
    systemctl enable --now "${SERVICES[@]}" 2>&1 | grep -v '^Created symlink' || true
    if [ "$builds_ok" = 1 ] && [ -f "$UNITS/ownpanel-build.service" ]; then
      systemctl start ownpanel-build-setup.service ownpanel-build.service 2>/dev/null || true
    fi
    wait_ready
    print_result
    return 0
  fi

  step "Plan ($os_name, linux/$arch)"
  printf '  %s\n' "${changes[@]}"
  [ "$installed" = 0 ] && say "  settings: OWNPANELD_ARGS=$new_args"
  [ "$installed" = 1 ] && say "  Your data in $DATA is kept. The services restart; sites are offline for a few seconds" &&
    say "  (other mail servers retry delivery on their own)."
  check_ports

  install_files
  setup_builds
  open_firewall
  wait_ready
  print_result
}

do_uninstall() {
  if [ "$opt_purge" = 1 ]; then
    say "This removes ownpanel and DELETES all its data: every site, the database,"
    say "certificates and the secret key in $DATA, all stored mail in $MAIL_DATA,"
    say "and the settings in $ETC."
    if [ "$opt_yes" = 0 ]; then
      interactive || die "--purge deletes all data; add --yes to confirm (questions are not asked when the script is piped in)"
      confirm "Delete everything?" n || die "cancelled; nothing was changed"
    fi
  fi

  step "Removing ownpanel"
  local s
  for s in ownpanel-update.path ownpanel-update.timer ownpanel-update.service; do
    if [ -f "$UNITS/$s" ]; then
      systemctl disable --now "$s" 2>&1 | grep -v '^Removed' || true
      rm -f "$UNITS/$s"
      say "Removed $s"
    fi
  done
  remove_builds
  for s in ownpaneld ownpanel-mail ownpanel-caddy; do
    if [ -f "$UNITS/$s.service" ]; then
      systemctl disable --now "$s" 2>&1 | grep -v '^Removed' || true
      rm -f "$UNITS/$s.service"
      say "Removed service $s"
    fi
  done
  systemctl daemon-reload 2>/dev/null || true
  for s in ownpaneld caddy; do
    if [ -f "$BIN/$s" ]; then
      rm -f "$BIN/$s"
      say "Removed $BIN/$s"
    fi
  done
  if [ -d "$LIB" ]; then
    rm -rf "$LIB"
    say "Removed $LIB"
  fi

  if [ "$opt_purge" = 1 ]; then
    rm -rf "$DATA" "$MAIL_DATA" "$ETC" "$UPDATE_ROOT" "$CADDY_DATA"
    say "Deleted $DATA, $MAIL_DATA, $CADDY_DATA, $UPDATE_ROOT and $ETC"
    if mountpoint -q "$BUILD_BASE/work" 2>/dev/null; then
      warn "$BUILD_BASE/work is still mounted; $BUILD_BASE was kept"
    elif [ -d "$BUILD_BASE" ]; then
      rm -rf -- "${BUILD_BASE:?}" && say "Deleted $BUILD_BASE"
    fi
    if id -u ownpanel >/dev/null 2>&1; then userdel ownpanel && say "Removed user ownpanel"; fi
    if id -u ownpanel-mail >/dev/null 2>&1; then userdel ownpanel-mail && say "Removed user ownpanel-mail"; fi
    if getent group "$MAIL_WEB_GROUP" >/dev/null; then groupdel "$MAIL_WEB_GROUP" && say "Removed group $MAIL_WEB_GROUP"; fi
    if id -u "$BUILD_USER" >/dev/null 2>&1; then
      pkill -KILL -u "$BUILD_USER" 2>/dev/null || true
      userdel "$BUILD_USER" && say "Removed user $BUILD_USER"
    fi
    # userdel of older shadow versions leaves the subordinate ids.
    sed -i "/^$BUILD_USER:/d" /etc/subuid /etc/subgid 2>/dev/null || true
    # The caddy user may belong to a Caddy package; only remove ours.
    case $(getent passwd caddy | cut -d: -f6) in
      "$DATA/caddy-home" | "$CADDY_DATA/home") userdel caddy && say "Removed user caddy" ;;
    esac
  else
    say "Kept your data in $DATA and $MAIL_DATA and the settings in $ETC."
    say "Installing again picks them up; --uninstall --purge deletes them."
  fi
  if command -v podman >/dev/null; then
    say "Podman and the other packages installed for builds were kept; remove them with"
    say "apt-get remove if nothing else uses them."
  fi
  say "Firewall rules and DNS records were not changed."
}

main() {
  parse_args "$@"
  [ "$(id -u)" = 0 ] || die "run as root, e.g. with sudo"
  if [ "$opt_uninstall" = 1 ]; then
    do_uninstall
  else
    do_install
  fi
}

# update.sh loads this file for its functions only.
if [ "${OWNPANEL_SOURCE_ONLY:-0}" != 1 ]; then
  main "$@"
  exit $?
fi
