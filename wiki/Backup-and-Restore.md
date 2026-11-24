# Backup and Restore

## Create a backup

```sh
sudo ownpaneld backup -data /var/lib/ownpanel -out /root/ownpanel-$(date +%F).tar.gz
```

| Flag | Default | |
|---|---|---|
| `-data` | `./data` | data directory of the daemon |
| `-out` | required | archive to write; must not exist (the command never overwrites) |

The archive is written with mode 0600. It can be made **while everything runs**: the database is copied with SQLite's `VACUUM INTO`, which gives a consistent snapshot. Run it as root (or as a user that can read everything in the data directory) so the certificates are readable.

### What it contains

| In the archive | What |
|---|---|
| `ownpanel.db` | a consistent copy of the database: users and password hashes, sessions, access token hashes, sites and domains, DNS connections (encrypted tokens), activity log |
| `secret.key` | the key that decrypts the stored DNS tokens |
| `sites/` | all site versions and the `current` links |
| `caddy/` | Caddy's storage: certificates and their private keys |

Not included: `setup-code` (only exists before the first admin), Caddy's home directory with its saved config (`caddy-home`; ownpaneld pushes the full config again on start), `/etc/ownpanel` (your flags in `ownpaneld.env`), the binaries and the systemd units. Keep a copy of your `ownpaneld.env` yourself.

### The backup contains secrets

With `secret.key` next to the database, anyone who has the archive can decrypt the stored DNS API tokens, and can use the private keys of the certificates. Keep the archive as safe as the server itself, and do not store it unencrypted somewhere less protected.

## Restore

On a server installed with `install.sh` (see [[Installation|Installation]]). Stop both services, remove the old database files, unpack and fix the owners:

```sh
systemctl stop ownpaneld ownpanel-caddy
rm -f /var/lib/ownpanel/ownpanel.db /var/lib/ownpanel/ownpanel.db-wal /var/lib/ownpanel/ownpanel.db-shm
tar -xzf ownpanel-2026-01-31.tar.gz -C /var/lib/ownpanel
chown -R ownpanel:ownpanel /var/lib/ownpanel/ownpanel.db /var/lib/ownpanel/sites /var/lib/ownpanel/secret.key
chown -R caddy:caddy /var/lib/ownpanel/caddy
systemctl start ownpanel-caddy ownpaneld
```

The leftover `-wal` and `-shm` files must go: SQLite would otherwise apply them to the restored database.

Notes:

- Unpacking over an existing `sites/` or `caddy/` directory adds and overwrites files but does not delete files that are not in the archive. For a clean restore, move the old `sites` and `caddy` directories away first.
- On a fresh server, run the installer first so users, units and directories exist, then restore, then restore your `ownpaneld.env` flags (`-server-ip` in particular if the address changed).
- Without `secret.key`, restored DNS connections cannot be used; add them again.
- Check that the sites and the panel answer, and sign in with an existing account.
