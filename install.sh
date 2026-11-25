#!/usr/bin/env bash
# Installs ownpanel (ownpaneld, its mail service and Caddy as systemd services) on Debian 12+ or
# Ubuntu 22.04+ (amd64, arm64). Run it again to upgrade in place.
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
# Requests from the panel to the root-side updater (see ownpanel-update).
UPDATE_DIR=$DATA/update
MAIL_DATA=/var/lib/ownpanel-mail
MAIL_ADMIN_ARG="-mail-admin unix:///run/ownpanel-mail/admin.sock"
DEFAULT_ARGS="-tls acme -caddy-admin unix:///run/ownpanel/caddy-admin.sock $MAIL_ADMIN_ARG"
SERVICES=(ownpanel-caddy ownpanel-mail ownpaneld)
UPDATE_UNITS=(ownpanel-update.service ownpanel-update.path ownpanel-update.timer)
# Files of a release, relative to its directory.
RELEASE_FILES=(ownpaneld caddy VERSION install.sh update.sh
  deploy/ownpaneld.service deploy/ownpanel-caddy.service deploy/ownpanel-mail.service deploy/caddy-bootstrap.json
  deploy/ownpanel-update deploy/ownpanel-update.service deploy/ownpanel-update.path deploy/ownpanel-update.timer)

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
  -y, --yes                 do not ask for confirmation (only --purge asks)
  --uninstall               stop and remove services and binaries; keeps data and settings
  --purge                   with --uninstall: also delete /var/lib/ownpanel, /var/lib/ownpanel-mail
                            (all stored mail), /etc/ownpanel and the users ownpanel,
                            ownpanel-mail and caddy
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

parse_args() {
  while [ $# -gt 0 ]; do
    case $1 in
      --channel) need_value "$@"; opt_channel=$2; shift ;;
      --version) need_value "$@"; opt_version=$2; shift ;;
      --panel-domain) need_value "$@"; opt_panel_domain=$2; shift ;;
      --server-ip) need_value "$@"; opt_server_ips+=("$2"); shift ;;
      --acme-email) need_value "$@"; opt_acme_email=$2; shift ;;
      --from-dir) need_value "$@"; opt_from_dir=$2; shift ;;
      --channel=* | --version=* | --panel-domain=* | --server-ip=* | --acme-email=* | --from-dir=*)
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
  for cmd in tar sha256sum install useradd usermod systemctl; do
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
  busy=$(ss -Hltnp '( sport = :25 or sport = :465 or sport = :587 or sport = :993 )' 2>/dev/null | grep -v '"ownpaneld"' || true)
  if [ -n "$busy" ]; then
    warn "another program listens on a mail port (25, 465, 587 or 993); ownpanel's e-mail cannot use it until that program is stopped (e.g. systemctl disable --now exim4 or postfix):"
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
  fi
  return 0
}

caddy_version() { "$1" version 2>/dev/null | awk '{print $1}' || echo unknown; }

install_files() {
  step "Installing"
  # Users. ownpaneld reaches Caddy's admin socket through the group caddy
  # (see ownpanel-caddy.service); Caddy's own directories stay 0700.
  id -u caddy >/dev/null 2>&1 || useradd --system --home "$DATA/caddy-home" --shell /usr/sbin/nologin caddy
  id -u ownpanel >/dev/null 2>&1 || useradd --system --home "$DATA" --shell /usr/sbin/nologin ownpanel
  usermod -a -G caddy ownpanel
  # The mail service runs as its own user; ownpaneld reaches its admin socket
  # through the group ownpanel-mail (see ownpanel-mail.service). The mail data
  # in $MAIL_DATA stays 0700, so the membership grants nothing else.
  id -u ownpanel-mail >/dev/null 2>&1 || useradd --system --user-group --home "$MAIL_DATA" --shell /usr/sbin/nologin ownpanel-mail
  usermod -a -G ownpanel-mail ownpanel

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
  install -d -m 0700 -o caddy -g caddy "$DATA/caddy" "$DATA/caddy-home"
  # The panel (group ownpanel) may create files here: requests for an update
  # and the automatic update setting. The sticky bit keeps it from replacing
  # or deleting the status file root writes into it.
  install -d -m 1770 -o root -g ownpanel "$UPDATE_DIR"
  install -d -m 0755 "$ETC"
  install -m 0644 "$src/deploy/caddy-bootstrap.json" "$ETC/caddy-bootstrap.json"
  # The files hold flags and the version, no secrets.
  env_content >"$ENV_FILE.new" && chmod 0644 "$ENV_FILE.new" && mv "$ENV_FILE.new" "$ENV_FILE"
  release_content >"$RELEASE_FILE.new" && chmod 0644 "$RELEASE_FILE.new" && mv "$RELEASE_FILE.new" "$RELEASE_FILE"

  # A config saved by an early version enabled the admin API on TCP and would
  # bring it back on --resume; drop it, ownpaneld pushes the full config again.
  local autosave=$DATA/caddy-home/caddy/autosave.json
  if [ -f "$autosave" ] && ! grep -q 'unix//' "$autosave"; then
    rm -f "$autosave"
  fi

  systemctl daemon-reload
  systemctl enable --now "${SERVICES[@]}" 2>&1 | grep -v '^Created symlink' || true
  systemctl enable --now ownpanel-update.path ownpanel-update.timer 2>&1 | grep -v '^Created symlink' || true
}

open_firewall() {
  if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active'; then
    local p
    for p in 80 443 25 465 587 993; do ufw allow "$p/tcp" >/dev/null; done
    say "Firewall (ufw): allowed 80, 443 (web) and 25, 465, 587, 993 (e-mail) over TCP."
  else
    say "Firewall: make sure 80, 443 (web) and 25, 465, 587, 993 (e-mail) are open over TCP"
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
  plan_changes

  if [ "$installed" = 1 ] && [ "${#changes[@]}" -eq 0 ]; then
    step "ownpanel $new_version is already installed with these settings; nothing to change"
    systemctl enable --now "${SERVICES[@]}" 2>&1 | grep -v '^Created symlink' || true
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
    rm -rf "$DATA" "$MAIL_DATA" "$ETC" /var/lib/ownpanel-update
    say "Deleted $DATA, $MAIL_DATA and $ETC"
    if id -u ownpanel >/dev/null 2>&1; then userdel ownpanel && say "Removed user ownpanel"; fi
    if id -u ownpanel-mail >/dev/null 2>&1; then userdel ownpanel-mail && say "Removed user ownpanel-mail"; fi
    # The caddy user may belong to a Caddy package; only remove ours.
    if [ "$(getent passwd caddy | cut -d: -f6)" = "$DATA/caddy-home" ]; then
      userdel caddy && say "Removed user caddy"
    fi
  else
    say "Kept your data in $DATA and $MAIL_DATA and the settings in $ETC."
    say "Installing again picks them up; --uninstall --purge deletes them."
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
