# Security

This page describes what ownpanel does to protect your server, in plain words, and what it does not do yet. It is based on the code; it is not an audit. ownpanel is early software ([[Home|Home]]).

## What is protected, and how

### Sign-in and sessions

- Passwords are stored as **argon2id** hashes. Passwords must be 10 to 200 characters.
- Sign-in sessions last 30 days. The cookie is `HttpOnly` and `SameSite=Strict`, and `Secure` when the request came over HTTPS. Only a SHA-256 hash of the session secret is stored.
- Changing a password ends the person's other sessions.
- A sign-in for a name that does not exist takes as long as one with a wrong password, and the message is the same.
- Failed sign-ins are throttled: 5 per name and address in 15 minutes ([[Users and Access|Users-and-Access]]).
- The first admin can only be created with a one-time setup code, kept in a file only the daemon's user can read, and compared in constant time. Once someone exists, setup is closed.

### Requests from browsers

- Changing requests made with the cookie must carry the header `X-Ownpanel-Request: 1`, which other web sites cannot send without a CORS preflight, and the API never allows CORS. A mismatching `Origin` is refused as well.
- The web interface is served with `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. API answers are `no-store`.
- MCP does not accept cookies at all, and refuses cross-site browser requests.

### Access tokens

- Tokens start with `op_` and are shown once. Only their SHA-256 hash is stored.
- A token can be read-only and limited to chosen sites, and never has more rights than its user. Revoking works at once.
- Tokens cannot create tokens or change passwords, and managing users needs an admin with a password sign-in or an unrestricted `write` token ([[Users and Access|Users-and-Access]]).
- Sites you may not see answer "not found", so names do not leak.
- Permission checks live in one place, `internal/panel`; the API and MCP only authenticate and translate ([[Architecture|Architecture]]).

### Uploaded archives

Archives are treated as untrusted. Only regular files and folders are accepted (no links), entry names are checked, writes cannot leave the version's directory, and the size and entry limits are enforced on the bytes actually read. See [[Sites and Publishing|Sites-and-Publishing]].

### The web server

- Caddy runs as its own system user. Its admin API is a **Unix socket** (never TCP), with mode 0660 in a directory only the `caddy` group can enter; only ownpaneld's user is in that group. On TCP, every local user could rewrite the web server's config.
- ownpaneld runs as its own, unprivileged user with `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=full` and `ProtectHome=true`. Caddy's data directories are mode 0700.
- The database file is created with mode 0600.

### Updates

Updating replaces binaries and restarts services, which needs root; the panel runs as `ownpanel` and must not get root. So the two only talk through files:

- The panel may only **ask**: it writes `request.json` (or `auto.json` for automatic updates) into `/var/lib/ownpanel/update`. That directory is `root:ownpanel` with mode 1770 (sticky), so the panel can create its own files but cannot replace or delete the status file root writes there.
- `ownpanel-update.service` (root, started by a path unit or a timer) first **moves** the request into a root-only directory, so it cannot be swapped for a link afterwards, then refuses anything that is not a small regular file owned by `ownpanel` and does not match exactly `{"id":"<32 hex>","channel":"stable|nightly"}` or `{"id":"<32 hex>","version":"vX.Y.Z"}`. The content of a refused request is never echoed. Only those values are passed to `update.sh`.
- Root writes `status.json` by creating a new file and renaming it into place (mode 0640, group `ownpanel`), never by opening an existing file the panel could have prepared.
- What gets installed is decided by GitHub's release and `checksums.txt`, downloaded by asset id, and the archive's `VERSION` must match; the panel can only choose the channel or a version name.
- Only admins (signed in, or with an unrestricted write token) can request updates or change automatic updates; read-only and site-limited tokens cannot. Every request and every result is in the activity log.
- The update unit runs with `NoNewPrivileges`, `PrivateTmp`, `PrivateDevices`, `ProtectHome`, `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectControlGroups`, `ProtectClock`, `ProtectHostname`, `RestrictSUIDSGID`, `RestrictNamespaces`, `RestrictRealtime`, `LockPersonality`, native system calls only and a short list of address families. It cannot use `ProtectSystem` or a capability limit: an update writes to `/usr/local`, `/etc/systemd/system` and `/etc/ownpanel`, may create users and open the firewall, just like `install.sh`.

### Stored secrets

- DNS API tokens are stored encrypted (AES-256-GCM) with a key in `secret.key` (mode 0600), never in the database. The API never returns them, and they are scrubbed from error messages and left out of the activity log. See [[DNS|DNS]].
- Passwords, session secrets and access tokens are never written to the log.
- A [[backup|Backup-and-Restore]] contains the database and the key together, so it must be protected like the server.

### E-mail

- The mail server runs as its own service and user (`ownpanel-mail`) with its own data in `/var/lib/ownpanel-mail` (0700). It cannot read the panel's database or `secret.key`; the panel cannot read stored mail. They talk only through a local socket of the group `ownpanel-mail`. The unit keeps only the capability to open ports below 1024, with a read-only system and no access to home directories.
- Mailbox passwords are argon2id hashes; mail programs sign in only over encrypted connections. Failed sign-ins are limited per address and network and per network, never per address alone (that would let anyone lock a mailbox). Unknown addresses take as long as wrong passwords.
- DKIM keys are stored encrypted with `secret.key`. Password hashes and keys never appear in API answers, logs or the activity log.
- A domain receives mail only once its MX record points at the mail server (or an administrator says so), so nobody can catch mail sent from this server to a domain they do not own.
- A mailbox can only send as its own address or forwarding addresses that deliver to it. Sending limits stop a hijacked mailbox; only administrators can allow it again.
- Accepted mail is written to disk before the server confirms it.
- Backups never read the mail data as root: `ownpaneld backup` reads it in a child process running as `ownpanel-mail` and keeps only plain files and folders from its output, so a taken-over mail service cannot make root copy or hand over other files through links.
- **Administrators can read any mailbox** by setting its password.

### Upload links

One-time links are valid for 15 minutes, for one site, and work once. They are re-checked against the creating token when used, so revoking the token ends them.

## Known gaps

- **Sign-in throttling is in memory.** It resets when ownpaneld restarts. It also trusts `X-Forwarded-For` from local connections (that is how it sees the visitor's address behind Caddy), so a local user can get around it.
- **The panel listens on plain HTTP** on a loopback port (`127.0.0.1:8600` by default). HTTPS exists only through Caddy with `-panel-domain`. Do not expose the port directly. Local users on the server can reach it.
- **No Content Security Policy** for the web interface yet (a test asserts the header is not set).
- **No per-site system users or quotas.** All site files belong to one user, and there is no limit on how many sites or how much disk space a person can use, beyond the per-upload limits.
- **Upload links are kept in memory**, so a restart invalidates them (safe, but surprising).
- **No second factor** for sign-in, and no list of a person's sessions to end them one by one; changing the password ends the others.
- **Access tokens do not expire.** Revoke them when they are no longer needed.
- **Not tested on a real server yet:** the installer, the updater and the hardening of the systemd units have not been verified in production ([[Roadmap|Roadmap]]).
- **E-mail has not been tested on a public server,** and there is no virus scanning. See [[E-Mail|E-Mail]].
- **A rollback does not restore the database.** If a new version migrated it and the old one cannot read it, restore the backup made before the update.

To report a problem, open an issue at <https://github.com/danielgtmn/ownpanel>.
