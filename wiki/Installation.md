# Installation

This page installs ownpanel on a Linux server with systemd. It uses the files in `deploy/`.

> The install script has not yet been tested on a real server. Try it on a throwaway machine first.

## What you need

- A Linux server with systemd and a public IPv4 and/or IPv6 address.
- Root access.
- A domain you control, for the panel itself (optional) and for your sites.
- The binaries `ownpaneld` and `caddy`, built for the server's architecture.

## 1. Build for Linux

On your development machine:

```sh
make build-linux
```

This builds the web app and writes `bin/linux-amd64/ownpaneld` (Linux, amd64, web interface embedded). The Makefile only builds amd64; for another architecture run the `go build -tags webui` command from the Makefile with a different `GOARCH` after `make web`.

You also need a `caddy` binary for the server. The project files do not say where to get it; `scripts/dev.sh` installs one for local use with `go install github.com/caddyserver/caddy/v2/cmd/caddy@latest`. The generated configuration uses only modules that belong to a standard Caddy build.

## 2. Copy the files

`install.sh` works in its own directory. It expects these files next to it: `ownpaneld`, `caddy`, `ownpaneld.service`, `ownpanel-caddy.service` and `caddy-bootstrap.json`.

```sh
mkdir -p stage
cp bin/linux-amd64/ownpaneld /path/to/caddy deploy/* stage/
scp -r stage root@server:/root/ownpanel-install
```

## 3. Run the installer

```sh
ssh root@server
cd /root/ownpanel-install
./install.sh
```

The script can be run again to upgrade. It stops on the first error (`set -e`).

### What it creates

| Item | Details |
|---|---|
| User `caddy` | system user, home `/var/lib/ownpanel/caddy-home`, shell `/usr/sbin/nologin` |
| User `ownpanel` | system user, home `/var/lib/ownpanel`, shell `/usr/sbin/nologin`; added to the group `caddy` |
| `/usr/local/bin/ownpaneld`, `/usr/local/bin/caddy` | the binaries (mode 0755) |
| `/etc/systemd/system/ownpaneld.service` | the panel daemon, runs as `ownpanel` |
| `/etc/systemd/system/ownpanel-caddy.service` | Caddy, runs as `caddy` |
| `/var/lib/ownpanel` | data directory, owned by `ownpanel` (0755) |
| `/var/lib/ownpanel/caddy`, `/var/lib/ownpanel/caddy-home` | Caddy's certificate storage and home, owned by `caddy`, mode 0700 |
| `/etc/ownpanel/caddy-bootstrap.json` | Caddy's start configuration (only the admin socket) |
| `/etc/ownpanel/ownpaneld.env` | the daemon's flags, see below |

It then enables and starts both services (`systemctl enable --now ownpanel-caddy ownpaneld`) and prints the status.

### The Caddy admin socket

Caddy's admin API is a Unix socket, never a TCP port, because on TCP every local user could rewrite the web server's config.

- `ownpanel-caddy.service` has `RuntimeDirectory=ownpanel` with mode 0750, so systemd creates `/run/ownpanel` as `caddy:caddy`.
- `caddy-bootstrap.json` makes Caddy create the socket `/run/ownpanel/caddy-admin.sock` with mode 0660, owned by the group `caddy`.
- The user `ownpanel` is a member of the group `caddy`, so ownpaneld can connect. Other local users can neither enter the directory nor open the socket.
- Caddy's own data directories are 0700, so the group membership gives `ownpanel` nothing else.

The units also set `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=full` and `ProtectHome=true`; Caddy gets `CAP_NET_BIND_SERVICE` to bind ports 80 and 443. Caddy is started with `--resume`, so after a restart it serves the last config until ownpaneld pushes the current one.

## 4. Choose the flags

ownpaneld reads its flags from `/etc/ownpanel/ownpaneld.env`, the variable `OWNPANELD_ARGS`. The installer writes this default if the file does not exist (or still holds the previous default `OWNPANELD_ARGS=-tls acme`) and leaves any other content alone:

```
OWNPANELD_ARGS=-tls acme -caddy-admin unix:///run/ownpanel/caddy-admin.sock
```

The service starts `/usr/local/bin/ownpaneld -data /var/lib/ownpanel $OWNPANELD_ARGS`. See [[Configuration|Configuration]] for all flags. The ones that matter on a server:

| Flag | Why |
|---|---|
| `-tls acme` | public certificates (this is the default of the flag and of the installer) |
| `-server-ip <address>` | the server's public address(es), repeatable or comma-separated. Without it, the domain check and "point to this server" report that the server's public address is not configured. Must be a public address unless `-tls` is `internal` or `off`. See [[DNS|DNS]] |
| `-panel-domain panel.example.com` | serve the panel itself over HTTPS on this domain, through Caddy with an automatic certificate. Without it the panel is only on `127.0.0.1:8600` |
| `-acme-email` | optional contact address for the certificate authority |

Example:

```
OWNPANELD_ARGS=-tls acme -caddy-admin unix:///run/ownpanel/caddy-admin.sock -panel-domain panel.example.com -server-ip 203.0.113.5,2001:db8::5
```

Apply a change with:

```sh
systemctl restart ownpaneld
```

No flag is strictly required: all have defaults. Without `-panel-domain` you can only reach the panel on the server itself, for example through an SSH tunnel to port 8600 (the installer does not set this up).

## 5. Firewall

The installer does not touch the firewall. Open:

| Port | Used for |
|---|---|
| 80/tcp | HTTP: redirects to HTTPS, and certificate validation |
| 443/tcp | HTTPS for your sites and the panel domain |

These are the defaults of `-http-port` and `-https-port`. Do not open port 8600; the panel listens on the loopback address only and Caddy reaches it from the same machine.

## 6. Create the first admin

Point the panel domain (or a site domain) at the server, then read the setup code:

```sh
sudo cat /var/lib/ownpanel/setup-code
```

Open the panel and create the first admin. Then create a site for a domain that points at the server; its certificate is requested automatically. See [[Getting Started|Getting-Started]] and [[Sites and Publishing|Sites-and-Publishing]].

## Upgrading

Build new binaries, copy them to the server together with `deploy/*` as in step 2, and run `./install.sh` again. It stops both services, replaces the binaries and unit files, reloads systemd and starts the services. Your data in `/var/lib/ownpanel` and your `ownpaneld.env` (unless it holds the old default) are kept. The database schema is migrated when the daemon starts.

Also: if a Caddy config saved by an older version still enables the TCP admin API (`autosave.json` in `/var/lib/ownpanel/caddy-home/caddy/` without a `unix//` address), the installer deletes it; ownpaneld pushes the full config again.

Back up before upgrading ([[Backup and Restore|Backup-and-Restore]]).

## Uninstalling

The project provides no uninstall script. Derived from what `install.sh` creates, a manual removal is:

```sh
systemctl disable --now ownpaneld ownpanel-caddy
rm /etc/systemd/system/ownpaneld.service /etc/systemd/system/ownpanel-caddy.service
systemctl daemon-reload
rm /usr/local/bin/ownpaneld /usr/local/bin/caddy
rm -r /etc/ownpanel
# This deletes all sites, the database and the secret key. Back up first.
rm -r /var/lib/ownpanel
userdel ownpanel
userdel caddy
```

`/usr/local/bin/caddy` may be used by something else on your machine; check before removing it. Domains and DNS records at Hetzner are not changed by ownpanel's removal.
