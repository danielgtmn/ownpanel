#!/usr/bin/env bash
# Installs ownpaneld and Caddy as systemd services on a Linux server. Safe to
# run again for upgrades.
# Run as root from a directory containing: ownpaneld, caddy, *.service,
# caddy-bootstrap.json
set -euo pipefail
cd "$(dirname "$0")"

id -u caddy  >/dev/null 2>&1 || useradd --system --home /var/lib/ownpanel/caddy-home --shell /usr/sbin/nologin caddy
id -u ownpanel >/dev/null 2>&1 || useradd --system --home /var/lib/ownpanel --shell /usr/sbin/nologin ownpanel
# ownpaneld reaches Caddy's admin socket through the caddy group (see
# ownpanel-caddy.service).
usermod -a -G caddy ownpanel

systemctl stop ownpaneld ownpanel-caddy 2>/dev/null || true
install -m 0755 ownpaneld caddy /usr/local/bin/
install -m 0644 ownpaneld.service ownpanel-caddy.service /etc/systemd/system/

install -d -m 0755 -o ownpanel -g ownpanel /var/lib/ownpanel
install -d -m 0700 -o caddy  -g caddy  /var/lib/ownpanel/caddy /var/lib/ownpanel/caddy-home
install -d -m 0755 /etc/ownpanel
install -m 0644 caddy-bootstrap.json /etc/ownpanel/caddy-bootstrap.json

args='OWNPANELD_ARGS=-tls acme -caddy-admin unix:///run/ownpanel/caddy-admin.sock'
env=/etc/ownpanel/ownpaneld.env
# Upgrade the previous default (admin API on TCP); keep anything customised.
if [ ! -f "$env" ] || grep -qx 'OWNPANELD_ARGS=-tls acme' "$env"; then
  echo "$args" > "$env"
fi
# A config saved by an older version would bring the TCP admin API back on
# --resume; drop it, ownpaneld pushes the full config again.
autosave=/var/lib/ownpanel/caddy-home/caddy/autosave.json
if [ -f "$autosave" ] && ! grep -q 'unix//' "$autosave"; then
  rm -f "$autosave"
fi

systemctl daemon-reload
systemctl enable --now ownpanel-caddy ownpaneld
systemctl --no-pager --lines=0 status ownpanel-caddy ownpaneld

if [ -f /var/lib/ownpanel/setup-code ]; then
  echo
  echo "First run: open the panel and create the first admin with the setup code:"
  echo "  sudo cat /var/lib/ownpanel/setup-code"
fi
