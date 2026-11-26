# Security

This page describes what ownpanel does to protect your server, in plain words, and what it does not do yet. It is based on the code; it is not an audit. ownpanel is early software ([[Home|Home]]).

## What is protected, and how

### Sign-in and sessions

- Passwords are stored as **argon2id** hashes. Passwords must be 10 to 200 characters. People sign in with their name or their e-mail address; there is no way to sign up from outside.
- A sign-in session ends after **3 days without use**, and in any case **30 days** after signing in. Only a SHA-256 hash of the session secret is stored.
- Over HTTPS the cookie is `__Host-ownpanel_session`: `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/` and no `Domain`, so no other host and no plain-HTTP page can set or overwrite it. A cookie under the old name `ownpanel_session` is ignored over HTTPS and removed. Plain HTTP (local development) uses `ownpanel_session`.
- Changing a password ends the person's other sessions; a password set by an admin or chosen with a link ends all of them. **Sign out everywhere** (account menu, or `POST /api/account/logout-everywhere`) ends all of them, the current one included.
- A sign-in for a name that does not exist takes as long as one with a wrong password, and the message is the same.
- **Forgot password** is offered only when the panel can send e-mail through its own mail server. The link goes only to the e-mail address stored for the person, and not at all when that address is in a mail domain of this panel that belongs to another member (it is then treated like an unknown address and admins see it in the activity log), works once and for 1 hour, and only a hash of it is stored. Asking for a link answers the same whether or not the address exists; links are limited to 3 per address and 20 requests per network address per hour. The link points at the panel domain as it is set when the link is requested, never at an address taken from the request.
- Failed sign-ins are throttled: 5 per account from any address, and 20 sign-ins per address across all names, in 15 minutes (counted per address as given; the setup code and the mail service count IPv6 per /48). The counts are kept in the database, so a restart does not reset them. At most 16 password checks run at the same time; further sign-ins are turned away (`429`) instead of piling up memory ([[Users and Access|Users-and-Access]]).
- The first admin can only be created with a one-time setup code, kept in a file only the daemon's user can read, and compared in constant time. Once someone exists, setup is closed.

### Requests from browsers

- Changing requests made with the cookie must carry the header `X-Ownpanel-Request: 1`, which other web sites cannot send without a CORS preflight, and the API never allows CORS. A mismatching `Origin` is refused as well.
- The web interface is served with `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. API answers are `no-store`.
- The web interface has a strict **Content Security Policy**:

  ```
  default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
  ```

  Scripts and style sheets only come from the panel itself; nothing inline is allowed, neither scripts nor styles, and there is no `'unsafe-inline'` or `'unsafe-eval'`. The page has no inline script. The UI library adds its CSS as constructed style sheets (an empty `csp-nonce` tag in `index.html` asks for that), which the policy does not block, so no nonce is needed. `data:` images are the small icons inside the panel's own style sheet. If someone managed to inject markup into the page, the browser would refuse to run scripts from it or send data to other sites.
- API answers carry `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` and `nosniff`, so an answer opened directly in the browser can neither load nor run anything. Site icons get their own policy that allows only the icon's inline styles.
- MCP does not accept cookies at all, and refuses cross-site browser requests.

### Access tokens

- Tokens start with `op_` and are shown once. Only their SHA-256 hash is stored.
- A token can be read-only and limited to chosen sites, and never has more rights than its user. Revoking works at once.
- A token can expire (30, 90 or 365 days, or never; the web interface suggests 90 days). An expired token is refused exactly like an unknown one.
- Tokens cannot create tokens or change passwords, and managing users needs an admin with a password sign-in or an unrestricted `write` token ([[Users and Access|Users-and-Access]]).
- Sites you may not see answer "not found", so names do not leak.
- Permission checks live in one place, `internal/panel`; the API and MCP only authenticate and translate ([[Architecture|Architecture]]).

### Uploaded archives

Archives are treated as untrusted. Only regular files and folders are accepted (no links), entry names are checked, writes cannot leave the version's directory, and the size and entry limits are enforced on the bytes actually read, including ignored entries. A zip's entry count is checked before its directory is loaded, so a large upload cannot make the panel hold its whole list of entries in memory. See [[Sites and Publishing|Sites-and-Publishing]].

### The web server

- Caddy runs as its own system user. Its admin API is a **Unix socket** (never TCP), with mode 0660 in a directory only the `caddy` group can enter; only ownpaneld's user is in that group. On TCP, every local user could rewrite the web server's config.
- ownpaneld runs as its own, unprivileged user with `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=full` and `ProtectHome=true`. Caddy's data directories are mode 0700.
- The database file is created with mode 0600.

### Slow and stalled connections

Time limits keep a slow or stalled client from holding connections open. Caddy applies them to everyone (sites and the panel share one server, so they suit both: large downloads and 512 MB uploads on slow links keep working as long as data keeps flowing):

| Caddy (every server) | Value |
|---|---|
| Request headers complete | 10 s |
| Request body stalls (no data arrives) | 1 min |
| Whole request, including the body | 1 h (512 MB at about 1.2 Mbit/s) |
| Response stalls (the client reads nothing) | 1 min |
| Kept-alive connection without a request | 2 min |

There is no limit on the whole response, so slow downloads of large files are not cut off; only a stalled one is.

Between Caddy and the panel: connecting 5 s; waiting for the panel's answer after the request has been passed on 10 min (time to unpack and publish an archive).

ownpaneld has its own limits, also when it is reached directly: request headers 10 s, a whole request 1 min, until the response is written 5 min, kept-alive connections 3 min (longer than Caddy keeps its idle connections to the panel, so Caddy closes them first). Archive uploads (`POST /api/sites/{name}/deployments`, upload links) and MCP requests get 1 h to arrive and 1 h 10 min until the answer.

### Updates

Updating replaces binaries and restarts services, which needs root; the panel runs as `ownpanel` and must not get root. So the two only talk through files:

- The panel may only **ask**: it writes `request.json` (or `auto.json` for automatic updates) into `/var/lib/ownpanel-update/requests`. That directory is `root:ownpanel` with mode 1770 (sticky) inside the root-owned `/var/lib/ownpanel-update`, so the panel can create its own files but cannot replace or delete the status file root writes there, nor the directory itself.
- Root never works by path inside a directory the panel's user owns. The panel's data directory `/var/lib/ownpanel` belongs to `ownpanel`, which could put a link at any name in it; so what root manages during an install or update lives elsewhere: the update requests and the backups in `/var/lib/ownpanel-update`, Caddy's certificates and home in `/var/lib/ownpanel-caddy` (owned by `caddy`). The backup before an update copies the database as `ownpanel` and writes the archive only into root's own directory.
- `ownpanel-update.service` (root, started by a path unit or a timer) first **moves** the request into a root-only directory, so it cannot be swapped for a link afterwards, then refuses anything that is not a small regular file owned by `ownpanel` (a file of any other owner, root included, is refused) and does not match exactly `{"id":"<32 hex>","channel":"stable|nightly"}` or `{"id":"<32 hex>","version":"vX.Y.Z"}`. The content of a refused request is never echoed. Only those values are passed to `update.sh`.
- Root writes `status.json` by creating a new file and renaming it into place (mode 0640, group `ownpanel`), never by opening an existing file the panel could have prepared. `auto.json` is read as `ownpanel` with a time limit, so a link or a pipe there gains nothing.
- What gets installed is decided by GitHub's release and `checksums.txt`, downloaded by asset id, and the archive's `VERSION` must match; the panel can only choose the channel or a version name.
- Only admins (signed in, or with an unrestricted write token) can request updates or change automatic updates; read-only and site-limited tokens cannot. Every request and every result is in the activity log.
- The update unit runs with `NoNewPrivileges`, `PrivateTmp`, `PrivateDevices`, `ProtectHome`, `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectControlGroups`, `ProtectClock`, `ProtectHostname`, `RestrictSUIDSGID`, `RestrictNamespaces`, `RestrictRealtime`, `LockPersonality`, native system calls only and a short list of address families. It cannot use `ProtectSystem` or a capability limit: an update writes to `/usr/local`, `/etc/systemd/system` and `/etc/ownpanel`, may create users and open the firewall, just like `install.sh`.

### Stored secrets

- DNS API tokens are stored encrypted (AES-256-GCM) with a key in `secret.key` (mode 0600), never in the database. The API never returns them, and they are scrubbed from error messages and left out of the activity log. See [[DNS|DNS]].
- Passwords, session secrets and access tokens are never written to the log.
- A [[backup|Backup-and-Restore]] contains the database and the key together, so it must be protected like the server.

### E-mail

- The mail server runs as its own service and user (`ownpanel-mail`) with its own data in `/var/lib/ownpanel-mail` (0700). It cannot read the panel's database or `secret.key`; the panel cannot read stored mail. They talk only through a local socket of the group `ownpanel-mail`. Caddy reaches the mail service only through a second socket, `/run/ownpanel-mail-web/web.sock` (group `ownpanel-mail-web`, of which only `caddy` is a member), which serves the mailbox users' account pages and nothing of the admin API; `X-Forwarded-For` is trusted only there. The unit keeps only the capability to open ports below 1024, with a read-only system and no access to home directories.
- Mailbox passwords are argon2id hashes; mail programs sign in only over encrypted connections. Failed sign-ins are limited per address and network and per network, never per address alone (that would let anyone lock a mailbox). Unknown addresses take as long as wrong passwords.
- DKIM keys are stored encrypted with `secret.key`. Password hashes and keys never appear in API answers, logs or the activity log.
- **App passwords** (`op_<id>_<secret>`, 160 random bits of secret) are shown once when created and never stored readable: the panel keeps only an HMAC-SHA-256 of the secret, bound to the mailbox and the id, under a server key that the panel makes once and keeps encrypted with `secret.key`. The panel pushes the key and the HMACs to the mail service, which looks an app password up by its id and checks it with one HMAC in constant time (never a slow password hash, so the time tells nothing about how many exist). They work for mail programs only. A revoked app password stops working at once, including in open connections. Their last use is recorded with the network only (/24 or /48), at most once an hour. No MCP tool creates, shows or revokes them, and neither app passwords nor their HMACs appear in answers after creation, logs or the activity log. A main password that looks like an app password is refused.
- **Only allow mail programs with app passwords** makes the mailbox's own password useless in mail programs; open connections signed in with it end.
- **The mailbox's own page** (`https://<mail server name>/`) is served by the mail service itself, not the panel: the panel only delivers the page's files and keeps no mailbox sessions, so a mistake in the panel cannot reach another mailbox. Only the mailbox password signs in there, never an app password, also with "only app passwords" on. Sign-ins have their own throttle (5 failures per address and network, 30 per network, in 15 minutes; IPv6 per /48; when the tables are full new sign-ins are refused) and their own password-check slot, so a flood there cannot slow mail programs, nor the other way round. Sessions are kept in the mail service's database as a SHA-256 hash of a random 256-bit id; the cookie `__Host-ownpanel_mailbox` is `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`. A session ends after 30 minutes without use, after 12 hours in any case, on sign-out, and at once when the mailbox is deleted or its password is no longer the one it signed in with (changed on another device, or reset in the panel). Changing requests need `X-Ownpanel-Request: 1` and are refused when `Origin` or `Sec-Fetch-Site` names another site; changes are limited to 30 per session in 15 minutes, request bodies to 16 KiB. Changing the password needs the current one. Changes reach the panel as events with the new hash or HMAC only; a new app password is returned once and never stored, logged or reported.
- **A reset in the panel wins at once:** when an administrator resets a mailbox's password (or changes its name or the "only app passwords" switch), the push tells the mail service when, and it drops what the mailbox changed itself before that moment, even if the panel has not seen that change yet. A change of one of these never undoes the mailbox's own change of another (an administrator renaming a mailbox does not bring back a password the mailbox replaced).
- **One mailbox cannot stop mail for everyone:** data of a single domain, mailbox or alias that the mail service would refuse is left out of the push (and logged) instead of stopping every update. The mailbox users' sign-in page failing to start never takes SMTP or IMAP down; Mail health shows it.
- **Organisations** are pushed to the mail service as random ids that say nothing about who owns the domains. Only administrators can group domains of different owners, and the activity log records it; the check and the change happen together. A member sees in their organisations only the domains they may see.
- A domain receives mail only once its MX record points at the mail server (or an administrator says so), so nobody can catch mail sent from this server to a domain they do not own.
- A mailbox can only send as its own address or forwarding addresses that deliver to it. Sending limits stop a hijacked mailbox; only administrators can allow it again.
- Accepted mail is written to disk before the server confirms it.
- Backups never read the mail data as root: `ownpaneld backup` reads it in a child process running as `ownpanel-mail` and keeps only plain files and folders from its output, so a taken-over mail service cannot make root copy or hand over other files through links.
- **Administrators can read any mailbox** by setting its password.
- **Download all mail** reads a whole mailbox without its user noticing, so only administrators and the owner of the mail domain may, signed in with a password or with an unrestricted write token (never read-only or site-limited tokens, never through MCP). Every download is noted in the activity log, which the domain's owner and administrators see; one runs per mailbox at a time.
- **Importing mail** takes another account's password. It goes to the mail service only, is kept in memory while copying, and is never stored, logged, returned or written to the activity log (it is asked for again after a restart). The mail service connects only to public addresses on ports 993 and 143, checks the other server's certificate and signs in only over an encrypted connection; the old account is opened read-only. Uploaded files are untrusted: zip files are checked for unsafe names, size and number of entries before anything is read, and an import stops at the mailbox's storage limit and before the disk runs low.

### Upload links

One-time links are valid for 15 minutes, for one site, and work once. They are re-checked against the creating token when used, so revoking the token ends them.

## Known gaps

- **Sign-in throttling trusts `X-Forwarded-For` from local connections** (that is how it sees the visitor's address behind Caddy), so a local user can get around the per-address limits.
- **Anyone who knows a name or e-mail address can lock that account for 15 minutes** with 5 wrong passwords, again and again. The limit is per account so that guessing from many addresses does not help; the price is this lockout.
- **Successful sign-ins count toward the limit of 20 per address**, so many people signing in from one shared address within 15 minutes can be turned away for a while.
- **The panel listens on plain HTTP** on a loopback port (`127.0.0.1:8600` by default). HTTPS exists only through Caddy with `-panel-domain`. Do not expose the port directly. Local users on the server can reach it.
- **No per-site system users or quotas.** All site files belong to one user, and there is no limit on how many sites or how much disk space a person can use, beyond the per-upload limits.
- **Upload links are kept in memory**, so a restart invalidates them (safe, but surprising).
- **No second factor** for sign-in, and no list of a person's sessions to end them one by one; "sign out everywhere" ends all of them.
- **Access tokens may be created without expiry.** Revoke those when they are no longer needed.
- **Not tested on a real server yet:** the installer, the updater and the hardening of the systemd units have not been verified in production ([[Roadmap|Roadmap]]).
- **E-mail has not been tested on a public server,** and there is no virus scanning. See [[E-Mail|E-Mail]].
- **A rollback does not restore the database.** If a new version migrated it and the old one cannot read it, restore the backup made before the update.

To report a problem, open an issue at <https://github.com/danielgtmn/ownpanel>.
