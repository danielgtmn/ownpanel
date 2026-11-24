# Installation

This page installs ownpanel on a Linux server with systemd: one command downloads a release, checks it, installs `ownpaneld` and Caddy as services and prints how to open the panel.

> The installer has been tested in systemd containers (Ubuntu 24.04, arm64), not yet on a real server. Try it on a throwaway machine first.

## What you need

- A server with **Debian 12+ or Ubuntu 22.04+**, systemd, **amd64 or arm64**, and a public IPv4 and/or IPv6 address.
- Root access.
- Ports **80/tcp and 443/tcp** reachable from the Internet.
- A domain you control, for the panel itself (optional) and for your sites.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh | sudo bash
```

When it is done, the installer prints:

- the address to open, `https://<public address of the server>/`. The panel has no domain yet, so it uses a self-signed certificate: accept the browser's warning once.
- the one-time **setup code**. Enter it in the panel, create the first administrator, then follow the setup steps: they ask for the panel's domain, the server's public addresses and the e-mail address for certificates.

The setup code is only valid until the first administrator exists. Show it again with `sudo cat /var/lib/ownpanel/setup-code`.

To set everything up front without the browser, pass the options (all optional):

```sh
curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh \
  | sudo bash -s -- --panel-domain panel.example.com --server-ip 203.0.113.5 --acme-email you@example.com
```

The installer never asks questions, except to confirm `--uninstall --purge`.

### Options

| Option | Meaning |
|---|---|
| `--channel stable\|nightly` | Release channel, see below. Default `stable`; upgrades keep the channel you chose. |
| `--version vX.Y.Z` | Install exactly this release. Wins over `--channel`. |
| `--panel-domain HOST` | Serve the panel on `https://HOST`. Sets `-panel-domain`. |
| `--server-ip ADDR` | Public IPv4/IPv6 address domains should point at. Repeat it or separate with commas. Sets `-server-ip`. |
| `--acme-email ADDR` | Contact address for the certificate authority. Sets `-acme-email`. |
| `--from-dir DIR` | Install from an unpacked release directory instead of downloading, e.g. on a server without Internet access. |
| `-y`, `--yes` | Do not ask for confirmation (only `--purge` asks). |
| `--uninstall` | Remove services and binaries, keep data and settings. |
| `--purge` | With `--uninstall`: also delete all data, settings and the users. |
| `-h`, `--help` | Show the options. |

### Release channels

| Channel | What it is |
|---|---|
| `stable` | Versioned releases (`v1.2.3`), made when `main` is merged into the `release` branch. The default. |
| `nightly` | An automatic build of `main`, made after every merged pull request that changes the program. For testing, not for production. |

Both are built by the Publish workflow (`.github/workflows/release.yml`) with `scripts/package.sh` and published as GitHub releases: stable releases under their tag, the newest one marked "latest", the nightly as the prerelease `nightly`, which is replaced by every new build. Each release has an archive per architecture, `ownpanel_<version>_linux_<arch>.tar.gz`, and `checksums.txt`. The installer refuses an archive whose SHA-256 does not match `checksums.txt`. How releases are made: [[Development|Development#releases]].

Switch channels by running the installer with the other `--channel`; it is stored in `/etc/ownpanel/release`.

### Without the one-liner

Download the archive for your architecture and `checksums.txt` from the [releases](https://github.com/danielgtmn/ownpanel/releases), check and unpack it, and run the installer that comes in it:

```sh
sha256sum --ignore-missing -c checksums.txt
tar -xzf ownpanel_v1.2.3_linux_amd64.tar.gz
sudo ./ownpanel_v1.2.3_linux_amd64/install.sh --from-dir ./ownpanel_v1.2.3_linux_amd64
```

To build an archive yourself: `make package VERSION=v1.2.3` (or `scripts/package.sh v1.2.3 amd64`) writes it to `dist/`. It needs Go, Node.js and `curl`; Caddy is downloaded from its official release and checked against a pinned SHA-512.

## First run

1. Open `https://<server address>/`. Until the panel has a domain, Caddy serves it there with a certificate from its own internal authority, so the browser warns once; accept it. Plain `http://` redirects to `https://`.
2. Enter the setup code and create the first administrator.
3. The setup steps ask for the panel's domain, the server's public addresses (the machine's own addresses are suggested) and an optional e-mail address for certificates. They are saved in the panel and take effect at once. The step shows whether the domain already points at the server and whether a trusted certificate is served for it.
4. Finish the setup. With a domain set, `https://<server address>/` (and `http://`) from then on redirect to `https://<panel domain>/`. Without a domain the panel stays reachable by address, with the self-signed certificate.

Values given as installer options (`--panel-domain`, `--server-ip`, `--acme-email`) become flags and win: the panel shows them as locked. Admins can change all other values later in the panel settings.

## What it creates

| Item | Details |
|---|---|
| User `caddy` | system user, home `/var/lib/ownpanel/caddy-home`, shell `/usr/sbin/nologin` |
| User `ownpanel` | system user, home `/var/lib/ownpanel`, shell `/usr/sbin/nologin`; added to the group `caddy` |
| `/usr/local/bin/ownpaneld`, `/usr/local/bin/caddy` | the binaries (mode 0755); an existing `/usr/local/bin/caddy` is replaced |
| `/etc/systemd/system/ownpaneld.service` | the panel daemon, runs as `ownpanel` |
| `/etc/systemd/system/ownpanel-caddy.service` | Caddy, runs as `caddy` |
| `/var/lib/ownpanel` | data directory, owned by `ownpanel` (0755) |
| `/var/lib/ownpanel/caddy`, `/var/lib/ownpanel/caddy-home` | Caddy's certificate storage and home, owned by `caddy`, mode 0700 |
| `/etc/ownpanel/caddy-bootstrap.json` | Caddy's start configuration (only the admin socket) |
| `/etc/ownpanel/ownpaneld.env` | the daemon's flags, see below |
| `/etc/ownpanel/release` | installed version and channel |

Both services are enabled and started. The installer waits until the panel answers on `127.0.0.1:8600`; if it does not within 60 seconds, it shows the daemon's log and fails.

### The Caddy admin socket

Caddy's admin API is a Unix socket, never a TCP port, because on TCP every local user could rewrite the web server's config.

- `ownpanel-caddy.service` has `RuntimeDirectory=ownpanel` with mode 0750, so systemd creates `/run/ownpanel` as `caddy:caddy`.
- `caddy-bootstrap.json` makes Caddy create the socket `/run/ownpanel/caddy-admin.sock` with mode 0660, owned by the group `caddy`.
- The user `ownpanel` is a member of the group `caddy`, so ownpaneld can connect. Other local users can neither enter the directory nor open the socket.
- Caddy's own data directories are 0700, so the group membership gives `ownpanel` nothing else.

The units also set `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=full` and `ProtectHome=true`; Caddy gets `CAP_NET_BIND_SERVICE` to bind ports 80 and 443. Caddy is started with `--resume`, so after a restart it serves the last config until ownpaneld pushes the current one.

## Flags

ownpaneld reads its flags from `/etc/ownpanel/ownpaneld.env`, the variable `OWNPANELD_ARGS`. A fresh install writes:

```
OWNPANELD_ARGS=-tls acme -caddy-admin unix:///run/ownpanel/caddy-admin.sock
```

plus `-panel-domain`, `-server-ip` and `-acme-email` if you gave the matching options. The service starts `/usr/local/bin/ownpaneld -data /var/lib/ownpanel $OWNPANELD_ARGS`. See [[Configuration|Configuration]] for all flags.

You can edit the file; apply a change with `systemctl restart ownpaneld`. The installer keeps your edits on upgrades and only replaces the flags you pass as options again.

## Firewall

If `ufw` is active, the installer allows 80/tcp and 443/tcp and says so. Otherwise it only reminds you; open them in your provider's firewall if it has one.

| Port | Used for |
|---|---|
| 80/tcp | HTTP: redirects to HTTPS, and certificate validation |
| 443/tcp | HTTPS for your sites, the panel domain, and the panel on the server's address during setup |

Do not open port 8600; the panel listens on the loopback address only and Caddy reaches it from the same machine.

## DNS for the panel domain

Point the panel domain at the server with an `A` record (IPv4) and/or `AAAA` record (IPv6), for example:

```
panel.example.com.  A     203.0.113.5
panel.example.com.  AAAA  2001:db8::5
```

The installer prints these records for the addresses it knows. The certificate is requested automatically as soon as the domain points at the server; nothing else to do.

## Without a public address: SSH tunnel

If the installer finds no public address (for example behind NAT), it prints an SSH tunnel instead. On your computer:

```sh
ssh -L 8600:127.0.0.1:8600 root@<server address>
```

Keep it open and browse to `http://127.0.0.1:8600`.

## Upgrading

Run the same command again:

```sh
curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh | sudo bash
```

It installs the newest release of your channel and lists what changes (version, Caddy, unit files, flags) before it stops both services, replaces the files and starts them again; sites are offline for a few seconds. If nothing changed, it leaves the services running. Your data in `/var/lib/ownpanel` and your `ownpaneld.env` are kept, except for flags you pass as options. The database schema is migrated when the daemon starts.

An early installer wrote `OWNPANELD_ARGS=-tls acme` (Caddy admin API on TCP); that is replaced by the current default, and a Caddy config saved by that version (`autosave.json` without a `unix//` address) is deleted, since ownpaneld pushes the full config again.

Back up before upgrading ([[Backup and Restore|Backup-and-Restore]]).

## Uninstalling

```sh
curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh | sudo bash -s -- --uninstall
```

This stops and removes both services and `/usr/local/bin/ownpaneld` and `/usr/local/bin/caddy`. It keeps `/var/lib/ownpanel` (sites, database, certificates, secret key), `/etc/ownpanel` and the users, so installing again brings everything back.

To delete everything, add `--purge`. It asks for confirmation; without a terminal it needs `--yes`:

```sh
curl -fsSL https://raw.githubusercontent.com/danielgtmn/ownpanel/main/install.sh | sudo bash -s -- --uninstall --purge --yes
```

This deletes `/var/lib/ownpanel` and `/etc/ownpanel` and removes the users `ownpanel` and `caddy` (the latter only if it was created for ownpanel). Back up first. Firewall rules and DNS records are not changed.
