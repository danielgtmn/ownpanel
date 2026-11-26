#!/usr/bin/env bash
# Updates an existing ownpanel installation to the newest release of its
# channel (or the one given), with a backup first and a rollback if the new
# version does not come up.
#
#   sudo /usr/local/lib/ownpanel/update.sh
#   curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/update.sh | sudo bash -s -- --yes
#
# It never asks setup questions and refuses to run where ownpanel is not
# installed; use install.sh for that. The release lookup, download, checks and
# the file layout live in install.sh: this script loads it for its functions
# and installs a release by running that release's own install.sh. The panel
# starts it through ownpanel-update.service (see deploy/ownpanel-update).
#
# Exit codes: 0 up to date or updated; 10 (--check) an update is available;
# 1 failed before anything was changed; 3 failed and rolled back to the
# previous version; 4 failed and the rollback failed too.
set -euo pipefail

INSTALLED_LIB=/usr/local/lib/ownpanel/install.sh
EXIT_AVAILABLE=10
EXIT_ROLLED_BACK=3
EXIT_BROKEN=4

# The functions of install.sh: next to this script (release archive, or the
# installed copy), else the installed copy (curl | bash), else the one on main.
load_installer() {
  local here="" lib=""
  if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
    here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
  fi
  if [ -n "$here" ] && [ -f "$here/install.sh" ]; then
    lib=$here/install.sh
  elif [ -f "$INSTALLED_LIB" ]; then
    lib=$INSTALLED_LIB
  else
    lib=$(mktemp)
    curl -fsSL --retry 3 --connect-timeout 15 --max-time 60 -o "$lib" "https://raw.githubusercontent.com/${OWNPANEL_REPO:-danielgtmn/ownpanel}/main/install.sh" ||
      { printf 'Error: could not download install.sh\n' >&2; exit 1; }
  fi
  # shellcheck source=install.sh
  OWNPANEL_SOURCE_ONLY=1 . "$lib"
}

load_installer

# Defined by install.sh; an installed install.sh of a version before the
# updater kept its files outside /var/lib/ownpanel does not have them.
UPDATE_ROOT=${UPDATE_ROOT:-/var/lib/ownpanel-update}
BACKUP_DIR=${BACKUP_DIR:-$UPDATE_ROOT/backups}
CADDY_DATA=${CADDY_DATA:-/var/lib/ownpanel-caddy}

# Defined after loading install.sh, which has its own.
usage() {
  cat <<'EOF'
Update an installed ownpanel.

  update.sh [options]

Options:
  --channel stable|nightly  release channel (default: the installed one)
  --version vX.Y.Z          update to exactly this release (wins over --channel)
  --check                   only show the installed and the available version;
                            exit 0 when up to date, 10 when an update is available
  -y, --yes                 do not ask for confirmation
  --force                   reinstall even if that version is installed already
  --from-dir DIR            update from an unpacked release directory instead of downloading
  --status-file FILE        write the progress as it goes (used by the panel)
  --no-rollback             keep a failed update in place (for tests)
  -h, --help                show this help

Before it changes anything it writes a backup to /var/lib/ownpanel-update/backups
(the last three are kept) and keeps the current files as *.previous. If the
panel does not answer with the new version within 60 seconds, the previous
version is put back.
EOF
}

# Options (install.sh defines opt_channel, opt_version, opt_from_dir, opt_yes).
opt_check=0
opt_force=0
opt_status_file=""
opt_request_id=""
opt_rollback=1
# The state and step last written to the status file (see finish).
reported=""
last_step=""

parse_args() {
  while [ $# -gt 0 ]; do
    case $1 in
      --channel) need_value "$@"; opt_channel=$2; shift ;;
      --version) need_value "$@"; opt_version=$2; shift ;;
      --from-dir) need_value "$@"; opt_from_dir=$2; shift ;;
      --status-file) need_value "$@"; opt_status_file=$2; shift ;;
      --request-id) need_value "$@"; opt_request_id=$2; shift ;;
      --channel=* | --version=* | --from-dir=* | --status-file=* | --request-id=*)
        set -- "${1%%=*}" "${1#*=}" "${@:2}"
        continue ;;
      --check) opt_check=1 ;;
      -y | --yes) opt_yes=1 ;;
      --force) opt_force=1 ;;
      --no-rollback) opt_rollback=0 ;;
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
  [[ $opt_request_id =~ ^([0-9a-f]{32}|auto)?$ ]] || die "--request-id must be 32 hex digits or 'auto'"
  if [ -n "$opt_status_file" ]; then
    [ -d "$(dirname "$opt_status_file")" ] || die "--status-file: $(dirname "$opt_status_file") is not a directory"
  fi
  return 0
}

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

json_escape() { printf '%s' "$1" | tr -d '\000-\037' | sed 's/\\/\\\\/g; s/"/\\"/g'; }

# report <state> <step> <message>: writes the status file, replacing it
# atomically (the panel reads it while this runs). Never fails the update.
report() {
  reported=$1
  [ "$1" != running ] || last_step=$2
  [ -n "$opt_status_file" ] && [ "$opt_check" = 0 ] || return 0
  local finished="" tmpf
  case $1 in succeeded | failed | rolled_back) finished=$(now) ;; esac
  {
    tmpf=$(mktemp "$(dirname "$opt_status_file")/.status.XXXXXX") &&
      printf '{"id":"%s","state":"%s","step":"%s","message":"%s","started_at":"%s","finished_at":"%s","from":"%s","to":"%s"}\n' \
        "$opt_request_id" "$1" "$2" "$(json_escape "$3")" "${started_at:-}" "$finished" \
        "$(json_escape "${old_version:-}")" "$(json_escape "${target:-}")" >"$tmpf" &&
      chmod 0640 "$tmpf" &&
      { chgrp ownpanel "$tmpf" 2>/dev/null || true; } &&
      mv -f "$tmpf" "$opt_status_file"
  } || warn "could not write the status file $opt_status_file"
}

# Every error from here on, also those raised in install.sh's functions, ends
# up in the status file.
die() {
  printf 'Error: %s\n' "$*" >&2
  report failed "" "$*"
  exit 1
}

# The status must never stay "running" once this script is gone: stopped by
# a signal (systemd stops the updater after 30 minutes, or someone presses
# Ctrl-C) or ended by an error that die did not catch, the last word is
# "failed". die, rollback and the end of main report a final state
# themselves, so then this does nothing.
# shellcheck disable=SC2329 # run by the traps in main
finish() {
  local rc=$? why=${1:-}
  trap - EXIT INT TERM
  if [ "${reported:-}" = running ]; then
    if [ -z "$why" ]; then why="The update stopped with an error (exit $rc)."; fi
    case $last_step in
      installing | verifying | rolling_back)
        why="$why It may be half installed: check that the panel answers; the backup is in $BACKUP_DIR." ;;
      *) why="$why Nothing was changed." ;;
    esac
    report failed "" "$why"
  fi
  # A download still running in the background (see download_asset).
  local job
  for job in $(jobs -p); do kill "$job" 2>/dev/null || true; done
  rm -rf "${tmp:-}"
  [ -z "${1:-}" ] || exit 1
  exit "$rc"
}

# shellcheck disable=SC2329 # run by the traps in main
stopped() {
  printf 'Error: stopped by %s\n' "$1" >&2
  finish "The update was stopped before it finished."
}

# release_moved is called by fetch_release (install.sh) when the nightly was
# replaced during the download and the newer build is downloaded instead.
release_moved() {
  target=$rel_version
  report running downloading "The nightly build changed meanwhile; downloading $target."
}

# Files an update replaces and a rollback puts back.
previous_files() {
  printf '%s\n' "$BIN/ownpaneld" "$BIN/caddy" "$UNITS/ownpaneld.service" "$UNITS/ownpanel-caddy.service" \
    "$UNITS/ownpanel-mail.service" "$UNITS/ownpanel-build.service" "$UNITS/ownpanel-build-setup.service" \
    "$ETC/caddy-bootstrap.json" "$RELEASE_FILE" "$ENV_FILE"
}

keep_previous() {
  local f
  while IFS= read -r f; do
    rm -f "$f.previous"
    if [ -f "$f" ]; then cp -p "$f" "$f.previous"; fi
  done < <(previous_files)
}

# The backup holds the database, the sites, the certificates and the secret
# key; root-only, the last three are kept. It goes to a directory only root
# can change. ownpaneld copies the database as the owner of the data
# directory, so root neither opens it nor leaves files of its own there.
do_backup() {
  local file bin=$BIN/ownpaneld args=()
  install -d -m 0755 -o root -g root "$UPDATE_ROOT"
  install -d -m 0700 -o root -g root "$BACKUP_DIR"
  # An ownpaneld from before that could not do so (updating by hand from an
  # old version): use the new release's for the backup.
  if ! "$bin" backup-db -h >/dev/null 2>&1; then
    bin=$UPDATE_ROOT/.ownpaneld-backup
    install -m 0755 -o root -g root "$src/ownpaneld" "$bin"
  fi
  # Caddy's certificates, once they live in their own directory.
  [ -d "$CADDY_DATA/storage" ] && [ ! -L "$CADDY_DATA/storage" ] && args+=(-caddy-storage "$CADDY_DATA/storage")
  file=$BACKUP_DIR/ownpanel-$(date -u +%Y%m%dT%H%M%SZ)-${old_version//[^A-Za-z0-9._-]/_}.tar.gz
  if ! "$bin" backup -data "$DATA" "${args[@]}" -out "$file" >/dev/null 2>&1; then
    rm -f "$UPDATE_ROOT/.ownpaneld-backup"
    die "the backup failed, so nothing was changed (try: $BIN/ownpaneld backup -data $DATA -out /root/ownpanel.tar.gz)"
  fi
  rm -f "$UPDATE_ROOT/.ownpaneld-backup"
  find "$BACKUP_DIR" -maxdepth 1 -name 'ownpanel-*.tar.gz' -printf '%T@ %p\n' | sort -rn | tail -n +4 | cut -d' ' -f2- |
    while IFS= read -r f; do rm -f -- "$f"; done
  say "Backup written: $file"
}

# healthy <version>: the installed binary is that version and the panel
# answers within 60 seconds.
healthy() {
  local v
  v=$("$BIN/ownpaneld" version 2>/dev/null || true)
  if [ "$v" != "$1" ]; then
    warn "ownpaneld reports version '$v', expected '$1'"
    return 1
  fi
  panel_ready 60
}

rollback() {
  step "Rolling back to ownpanel $old_version"
  report running rolling_back "The new version did not start; going back to $old_version."
  systemctl stop ownpaneld ownpanel-mail ownpanel-caddy ownpanel-build 2>/dev/null || true
  local f
  while IFS= read -r f; do
    if [ -f "$f.previous" ]; then
      cp -p "$f.previous" "$f.rollback" && mv -f "$f.rollback" "$f"
    fi
  done < <(previous_files)
  # A version from before the mail service has no mail unit to go back to.
  if [ ! -f "$UNITS/ownpanel-mail.service.previous" ] && [ -f "$UNITS/ownpanel-mail.service" ]; then
    systemctl disable ownpanel-mail 2>/dev/null || true
    rm -f "$UNITS/ownpanel-mail.service"
  fi
  # Nor a version from before builds a build service.
  if [ ! -f "$UNITS/ownpanel-build.service.previous" ] && [ -f "$UNITS/ownpanel-build.service" ]; then
    systemctl disable --now ownpanel-build ownpanel-build-setup 2>/dev/null || true
    rm -f -- "${UNITS:?}/ownpanel-build.service" "${UNITS:?}/ownpanel-build-setup.service" "${UNITS:?}/nftables.service.d/ownpanel-build.conf"
  fi
  systemctl daemon-reload
  systemctl start ownpanel-caddy ownpaneld || true
  [ -f "$UNITS/ownpanel-mail.service" ] && { systemctl start ownpanel-mail || true; }
  [ -f "$UNITS/ownpanel-build.service" ] && { systemctl start ownpanel-build-setup ownpanel-build || true; }
  if healthy "$old_version"; then
    say "ownpanel $old_version is running again."
    report rolled_back "" "The update to $target failed, so $old_version was put back. $1"
    exit "$EXIT_ROLLED_BACK"
  fi
  systemctl --no-pager --lines=0 status "${SERVICES[@]}" >&2 || true
  journalctl --no-pager -u ownpaneld -n 20 >&2 || true
  report failed "" "The update to $target failed and $old_version could not be started again. Restore the backup in $BACKUP_DIR. $1"
  printf 'Error: the rollback failed too; restore the backup in %s\n' "$BACKUP_DIR" >&2
  exit "$EXIT_BROKEN"
}

main() {
  parse_args "$@"
  if [ "$opt_check" = 0 ] && [ "$(id -u)" != 0 ]; then
    die "run as root, e.g. with sudo"
  fi
  if [ ! -f "$RELEASE_FILE" ] || [ ! -f "$UNITS/ownpaneld.service" ]; then
    die "ownpanel is not installed here ($RELEASE_FILE or the ownpaneld service is missing); install it with install.sh"
  fi
  started_at=$(now)
  target=""
  check_system
  read_installed
  new_args=${old_args:-$DEFAULT_ARGS}
  channel=${opt_channel:-${old_channel:-stable}}
  if [ -n "$opt_version" ]; then
    case $opt_version in nightly*) channel=nightly ;; *) channel=stable ;; esac
  fi

  tmp=$(mktemp -d)
  trap finish EXIT
  trap 'stopped SIGTERM' TERM
  trap 'stopped SIGINT' INT
  report running checking "Looking for the new version."

  if [ -n "$opt_from_dir" ]; then
    [ -d "$opt_from_dir" ] || die "--from-dir: $opt_from_dir is not a directory"
    src=$(cd "$opt_from_dir" && pwd)
    check_release
    target=$new_version
    case $target in nightly-*) channel=nightly ;; *) channel=stable ;; esac
  else
    lookup_release "$(release_tag)"
    target=$rel_version
  fi

  say "Installed: ownpanel $old_version (${old_channel:-stable})"
  say "Available: ownpanel $target ($channel)"
  if [ "$opt_check" = 1 ]; then
    if [ "$target" = "$old_version" ]; then
      say "Up to date."
      return 0
    fi
    say "An update is available."
    exit "$EXIT_AVAILABLE"
  fi
  if [ "$target" = "$old_version" ] && [ "$opt_force" = 0 ]; then
    say "Up to date; nothing to do (--force reinstalls it)."
    report succeeded "" "ownpanel $target is already installed."
    return 0
  fi
  if [ "$opt_yes" = 0 ] && interactive; then
    confirm "Update ownpanel $old_version to $target? Sites are offline for a few seconds." y || die "cancelled; nothing was changed"
  elif [ "$opt_yes" = 0 ]; then
    # Piped in (curl … | sudo bash): running the command is the confirmation.
    say "Updating now; sites are offline for a few seconds."
  fi

  if [ -z "$opt_from_dir" ]; then
    report running downloading "Downloading $target."
    fetch_release
    check_release "$rel_version"
  fi

  step "Backing up"
  report running backing_up "Backing up the panel's data."
  do_backup
  keep_previous

  step "Installing ownpanel $target"
  report running installing "Installing $target; the panel restarts."
  local rc=0 why=""
  bash "$src/install.sh" --from-dir "$src" --yes || rc=$?
  if [ "$rc" != 0 ]; then
    why="The installer stopped with an error (exit $rc)."
  else
    report running verifying "Checking that $target is running."
    healthy "$target" || { rc=1; why="The panel did not answer with $target within 60 seconds."; }
  fi
  if [ "$rc" != 0 ]; then
    if [ "$opt_rollback" = 0 ]; then
      report failed "" "$why Nothing was rolled back."
      printf 'Error: %s Nothing was rolled back (--no-rollback).\n' "$why" >&2
      exit 1
    fi
    rollback "$why"
  fi

  step "ownpanel $old_version -> $target: done"
  report succeeded "" "Updated from $old_version to $target."
}

main "$@"
exit $?
