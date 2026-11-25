# Backup and Restore

## Create a backup

```sh
sudo ownpaneld backup -data /var/lib/ownpanel -out /root/ownpanel-$(date +%F).tar.gz
```

| Flag | Default | |
|---|---|---|
| `-data` | `./data` | data directory of the daemon |
| `-out` | required | archive to write; must not exist (the command never overwrites) |
| `-mail-data` | `/var/lib/ownpanel-mail` | data directory of the mail service; skipped if it does not exist |
| `-no-mail` | off | leave the stored mail out (it can be large; back it up separately) |

The archive is written with mode 0600. It can be made **while everything runs**: the database is copied with SQLite's `VACUUM INTO`, which gives a consistent snapshot. Run it as root (or as a user that can read everything in the data directory) so the certificates are readable.

### What it contains

| In the archive | What |
|---|---|
| `ownpanel.db` | a consistent copy of the database: users and password hashes, sessions, access token hashes, sites and domains, DNS connections (encrypted tokens), activity log |
| `secret.key` | the key that decrypts the stored DNS tokens |
| `sites/` | all site versions and the `current` links |
| `caddy/` | Caddy's storage: certificates and their private keys |
| `mail-tls/` | the mail server's certificate and its account at the certificate authority |
| `mail/` | the mail service's data: a consistent copy of its index (`mail.db`), every stored message, its last configuration (with mailbox password hashes and DKIM keys) and an empty file `restored` |

Not included: `setup-code` (only exists before the first admin), Caddy's home directory with its saved config (`caddy-home`; ownpaneld pushes the full config again on start), `/etc/ownpanel` (your flags in `ownpaneld.env`), the binaries and the systemd units. Keep a copy of your `ownpaneld.env` yourself.

### The backup contains secrets

With `secret.key` next to the database, anyone who has the archive can decrypt the stored DNS API tokens, and can use the private keys of the certificates. Keep the archive as safe as the server itself, and do not store it unencrypted somewhere less protected.

## Restore

On a server installed with `install.sh` (see [[Installation|Installation]]). Stop the services, remove the old database files, unpack and fix the owners:

```sh
systemctl stop ownpaneld ownpanel-mail ownpanel-caddy
rm -f /var/lib/ownpanel/ownpanel.db /var/lib/ownpanel/ownpanel.db-wal /var/lib/ownpanel/ownpanel.db-shm
rm -f /var/lib/ownpanel-mail/mail.db /var/lib/ownpanel-mail/mail.db-wal /var/lib/ownpanel-mail/mail.db-shm
tar -xzf ownpanel-2026-01-31.tar.gz -C /var/lib/ownpanel --exclude 'mail/*' --exclude mail
tar -xzf ownpanel-2026-01-31.tar.gz -C /var/lib/ownpanel-mail --strip-components 1 mail
(cd /var/lib/ownpanel && chown -R ownpanel:ownpanel ownpanel.db secret.key $(ls -d sites mail-tls 2>/dev/null))
chown -R caddy:caddy /var/lib/ownpanel/caddy
chown -R ownpanel-mail:ownpanel-mail /var/lib/ownpanel-mail
systemctl start ownpanel-caddy ownpanel-mail ownpaneld
```

The leftover `-wal` and `-shm` files must go: SQLite would otherwise apply them to the restored database. Skip the mail lines for an archive made with `-no-mail`.

The file `restored` tells the mail service that its data is older than what mail programs may have seen: it renumbers every folder once, so mail programs load the folders again instead of showing messages that are not there any more. Mail that arrived after the backup is lost unless the sending servers deliver it again.

Notes:

- Unpacking over an existing `sites/` or `caddy/` directory adds and overwrites files but does not delete files that are not in the archive. For a clean restore, move the old `sites` and `caddy` directories away first.
- On a fresh server, run the installer first so users, units and directories exist, then restore, then restore your `ownpaneld.env` flags (`-server-ip` in particular if the address changed).
- Without `secret.key`, restored DNS connections cannot be used; add them again.
- Check that the sites and the panel answer, and sign in with an existing account.
- A backup made with an older version can be restored into a newer one: the database is brought up to date when ownpaneld starts. The other direction is not supported.
- Every update saves a backup first, in `/var/lib/ownpanel/backups/`. Restore one of those the same way.
