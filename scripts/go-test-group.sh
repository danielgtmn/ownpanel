#!/usr/bin/env bash
# Runs one group of the Go tests with the race detector, so CI can run the
# groups in parallel (the "go (<group>)" jobs in .github/workflows/ci.yml).
#
#   scripts/go-test-group.sh <group>     run the tests of one group
#   scripts/go-test-group.sh list <group> print what the group runs
#   scripts/go-test-group.sh groups      print the names of all groups
#   scripts/go-test-group.sh check       prove that the groups together run
#                                        every package and every panel test
#                                        exactly once
#
# Groups:
#   panel-1 … panel-N  internal/panel, its top-level tests split into N
#                      shards (it is by far the slowest package)
#   mail               internal/mail/...
#   rest               every other package of `go list ./...`, so a new
#                      package is never left out
#
# The panel shards are computed from `go test -list`: names are sorted, the
# slow permission matrices (names containing "Permission") come first, then
# the names are dealt out in turn. A new test lands in some shard by itself.
set -euo pipefail

cd "$(dirname "$0")/.."

panel_shards=4
module="$(go list -m)"
panel="$module/internal/panel"

groups() {
	local i
	for ((i = 1; i <= panel_shards; i++)); do echo "panel-$i"; done
	echo mail
	echo rest
}

all_packages() { go list ./...; }
mail_packages() { go list ./internal/mail/...; }

# The top-level tests, examples and fuzz targets of internal/panel, sorted.
panel_tests() {
	go test -list . ./internal/panel | grep -v '^ok ' | LC_ALL=C sort
}

# panel_shard <n>: the panel tests of shard n (1-based).
panel_shard() {
	local n="$1" tests
	tests="$(panel_tests)"
	{
		grep 'Permission' <<<"$tests" || true
		grep -v 'Permission' <<<"$tests" || true
	} | awk -v n="$n" -v m="$panel_shards" 'NF && (NR - 1) % m == n - 1'
}

rest_packages() {
	all_packages | grep -vxF -e "$panel" -f <(mail_packages)
}

# list <group>: one line per package, and for panel shards one line
# "<package> <test>" per test.
list() {
	case "$1" in
	panel-*)
		local n="${1#panel-}"
		if ! [[ "$n" =~ ^[0-9]+$ ]] || ((n < 1 || n > panel_shards)); then
			echo "unknown group: $1" >&2
			exit 2
		fi
		panel_shard "$n" | sed "s|^|$panel |"
		;;
	mail) mail_packages ;;
	rest) rest_packages ;;
	*)
		echo "unknown group: $1" >&2
		exit 2
		;;
	esac
}

run() {
	local group="$1"
	case "$group" in
	panel-*)
		local tests
		tests="$(list "$group" | cut -d' ' -f2 | paste -sd'|' -)"
		if [ -z "$tests" ]; then
			echo "No tests in $group."
			exit 0
		fi
		go test -race -count=1 -run "^($tests)\$" ./internal/panel
		;;
	*)
		local pkgs
		pkgs="$(list "$group")"
		# Package paths contain no spaces.
		# shellcheck disable=SC2086
		go test -race -count=1 $pkgs
		;;
	esac
}

check() {
	local want got dup g
	got="$(for g in $(groups); do list "$g"; done)"
	dup="$(LC_ALL=C sort <<<"$got" | uniq -d)"
	if [ -n "$dup" ]; then
		echo "In more than one group:" >&2
		echo "$dup" >&2
		exit 1
	fi
	# Every package once (the panel shards count as the panel package), and
	# every panel test once.
	want="$(
		all_packages | grep -vxF "$panel" || true
		panel_tests | sed "s|^|$panel |"
	)"
	got="$(grep -v "^$panel\$" <<<"$got")"
	if ! diff <(LC_ALL=C sort <<<"$want") <(LC_ALL=C sort <<<"$got"); then
		echo "The groups do not run exactly every package and panel test (< missing, > extra)." >&2
		exit 1
	fi
	echo "$(wc -l <<<"$got") packages and panel tests, each in exactly one of: $(groups | paste -sd' ' -)"
}

case "${1:-}" in
groups) groups ;;
list) list "${2:?group}" ;;
check) check ;;
"" | -h | --help)
	sed -n '2,/^set /p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
	exit 2
	;;
*) run "$1" ;;
esac
