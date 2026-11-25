# Groupware architecture

Status: **plan**, not built (reviewed once against the code and the
protocols). Tracked in [#84](https://github.com/danielgtmn/ownpanel/issues/84). It builds on [[E-Mail architecture|E-Mail-Architecture]] and
follows the same rules: self-built in Go on top of libraries, inside the
mail service `ownpanel-mail`, no ready-made groupware product.

## Goal in one paragraph

Teams using ownpanel mail should get what they use Exchange for, as far as
open protocols carry it:
- **shared mailboxes** (info@, support@) that several people read with their own sign-in;
- **sending as**, or **on behalf of**, such an address;
- **distribution groups**;
- **filters and an out-of-office reply**;
- **calendars and address books** that sync to phones and desktop programs, can be shared, and come with a company directory;
- later, **invitations, free/busy and rooms**.

It all works with standard programs: Thunderbird, Apple Mail/Calendar/Contacts, iOS, Android with DAVx5, and Outlook with limits. Exchange's own protocols are **not** in scope, see [Not doing](#not-doing).

## What exists today

- A **mailbox** is an address with a password, an argon2id hash that the
  panel keeps and pushes to the mail service. Mailbox users have no other
  identity: they sign in to IMAP/SMTP, nothing else.
- **Panel users** (admins and members) own mail domains and manage
  mailboxes. They are operators, not necessarily the people who read the
  mail; a member may host several customers.
- The mail service owns all mail data. The panel owns the desired state and
  pushes it as one configuration that carries no owner information.

## Decisions

1. **The mailbox is the person.** No new user directory. A personal mailbox
   (anna@example.com) is the identity for everything groupware: IMAP, DAV,
   sharing and the self-service area. Sign-in is always the full address. Mailboxes get a
   **display name** (G1), which invitations, out-of-office replies and the
   directory need.
2. **The sharing boundary is the organisation, not the panel user.** By
   default every **mail domain is its own organisation**. A domain owner can
   group several of their domains into one organisation, for example a company
   with `example.com` and `example.de`. Grants, self-delegation, calendar
   and address-book sharing, free/busy and the directory never cross an
   organisation. Admins cannot cross it either: they can only regroup
   domains, and that is logged. A member's customers are separate
   organisations, so they never see each other.
3. **The mail service enforces the boundary itself.** The configuration
   carries an opaque `Organisation` id per domain. The mail service checks
   it on every access, not only when a share is created. At config load it
   disables shares that no longer fit: a domain moved to another
   organisation, a mailbox deleted, a personal mailbox made shared.
4. **Two kinds of state, as today:**
   - **Administered** state lives in the panel database and is pushed in the configuration:
     organisations, mailbox kind (personal, shared, resource), display name,
     grants on shared mailboxes, distribution groups, app-password records.
   - **User data** lives in the mail service (`mail.db` and files): filter scripts,
     out-of-office, calendars, events, address books, contacts, sharing of
     calendars and address books, and per-user read state. The panel never stores it;
     backups include it with the mail data.
5. **Everything groupware runs in the mail service**, like mail. It has the
   data, the sign-in throttle and the backup path, and it runs without the
   panel: calendars keep syncing while the panel is stopped.
6. **Self-service data is served by the mail service** and authenticated by
   its own sign-in. The trust model: the panel serves only the self-service
   **web app** (static files) and keeps no mailbox sessions for data. The
   mail service's `/account/…` API authenticates the mailbox itself, so a
   scoping bug in the panel cannot reach another mailbox. Changes to
   administered state (password, app passwords, display name) are made
   there too: the mail service checks the request, applies the change at
   once and reports it as an **event** with the new hash, never the
   password. The panel already polls events; it records the change in its
   database and in the activity log, so the next configuration push keeps
   it. Until the panel has seen the event, a push still carries the old
   value, so the mail service keeps changes until the panel's
   configuration includes them, compared by a per-mailbox change counter.
   No socket is needed from the mail service to the panel.
7. **One web address for the people who read mail: the mail server name**
   (`https://mail.example.com`). It serves the self-service area, DAV and,
   later, webmail. **Groupware requires `mail_host` ≠ the panel domain**;
   the settings refuse to turn groupware on otherwise. See
   [G0](#g0--web-entry-point-on-the-mail-host).

## Phases

| Phase | Content | Size | Depends on | Issue |
|---|---|---|---|---|
| G0 | Web entry point on the mail host: Caddy site, certificate, routing, DAV socket | S | — | [#85](https://github.com/danielgtmn/ownpanel/issues/85) |
| G1 | Mailbox accounts: display name, self-service area, app passwords, organisations (built) | M | G0 | [#86](https://github.com/danielgtmn/ownpanel/issues/86) |
| G2 | Filters and out-of-office (Sieve, ManageSieve) | L | G1 | [#87](https://github.com/danielgtmn/ownpanel/issues/87) |
| G3 | Shared mailboxes, send as / on behalf, distribution groups | XL | G1 | [#88](https://github.com/danielgtmn/ownpanel/issues/88) |
| G4a | Own calendars and contacts, discovery, sync, directory (built) | XL | G1 | [#89](https://github.com/danielgtmn/ownpanel/issues/89) |
| G4b | Sharing calendars and address books | M | G4a | [#90](https://github.com/danielgtmn/ownpanel/issues/90) |
| G5a | Free/busy, sending invitations, accepting replies | L | G4a | [#91](https://github.com/danielgtmn/ownpanel/issues/91) |
| G5b | Server-side scheduling between mailboxes, rooms | XL | G5a | [#92](https://github.com/danielgtmn/ownpanel/issues/92) |
| G6 | Webmail, web calendar and contacts | XL | G1–G4 | [#30](https://github.com/danielgtmn/ownpanel/issues/30) |

Each phase is its own PR or a few, and each ships alone. G2, G3 and G4a can
run in parallel after G1. Before building G2, G4a and G5, a **library
spike** decides library vs. own code (see [Libraries](#libraries)).

## G0 — Web entry point on the mail host

- **Caddy gets a site for the mail host**:
  - `/dav/…`, `/.well-known/caldav`, `/.well-known/carddav` and `/account/api/…` go to the mail service's **web socket** (a Unix socket);
  - everything else goes to the panel, which serves only the self-service web app (and webmail later).

  The panel's own API and admin pages are not routed on this host. Routing is
  by host, so the panel domain never answers `/account/api` either.
- **The web socket is not the admin socket:** it lives in
  `/run/ownpanel-mail-web/web.sock`, in its own directory and group, which
  Caddy's user joins. Caddy can reach DAV and the account API but never
  `admin.sock` in `/run/ownpanel-mail` (0750, group `ownpanel-mail`). The
  mail service trusts `X-Forwarded-For` only on that socket, for its
  sign-in throttle.
- **Certificate:** the mail service keeps getting the mail host's
  certificate (autocert, as today), and Caddy **loads that same
  certificate** (`tls.certificates.load_pem`, automation off for the name)
  instead of requesting a second one. The panel pushes the PEM when it
  renews. This keeps the E-Mail plan's rule that only one ACME client owns
  the name; that page is updated to describe the new `:443` matcher.
- **Request limits:** request-size limits in Caddy. Rate limits live in the
  mail service, because standard Caddy has no rate-limit module.

**Built (G0)** — how it turned out:

- The mail service opens the web socket with `ownpaneld mail -web
  unix:///run/ownpanel-mail-web/web.sock` (package `internal/mail/web`):
  `GET /account/api/health` answers `{"status":"ok"}`; `/dav/…` answers
  501 and `/.well-known/caldav|carddav` 404 until G4. Limits: 10 s for
  headers, 2 min per request, 64 KB of headers, 16 MB of body.
- **Hooks for G1:** `server.Options.AccountHandler` (an `http.Handler`)
  serves everything below `/account/api/` except `health`, with the full
  path. `web.ClientIP(r)` returns the client's address: the right-most
  `X-Forwarded-For` entry, the hop Caddy appended (Caddy replaces what the
  client sent). It is set only on the web socket; `ok == false` means
  unknown, never trusted.
- `ownpanel-mail.service` lists both runtime directories
  (`RuntimeDirectory=ownpanel-mail ownpanel-mail-web`, 0750). systemd
  resets their owner and mode before every command it starts, so the
  service itself hands `/run/ownpanel-mail-web` and `web.sock` to the group
  `ownpanel-mail-web` (`-web-group`); it is a member through
  `SupplementaryGroups=`, and `caddy` is the only other member.
- If the web socket cannot be set up (the group is missing, handing it
  over fails, another file is in the way) or stops, the mail service logs
  it, records a `listener.failed` event and runs on without it: SMTP and
  IMAP never depend on it. `/status` reports it as `web` (`listening`,
  `error`), and the Mail health check `self_service` turns into an error.
- The panel learns the socket from `-mail-web` and adds the Caddy site only
  when `mail_host` differs from the panel domain and the certificate is
  there. Request limits in Caddy: 16 MB to the mail service, 64 KB to the
  web app.
- The panel answers requests for `mail_host` with the web app only (`/api`
  and `/mcp` are 404 there too). `index.html` then carries
  `<meta name="ownpanel-mail-host">`, which makes the app show the account
  area (`web/src/account/`) instead of the panel.
- The settings do not refuse `mail_host` = panel domain yet (there is no
  groupware switch to refuse); Mail health has the check `self_service`,
  a warning in that case.

## G1 — Mailbox accounts

**Display name** per mailbox: set by the domain owner and changeable by the
person.

**Organisations:** grouping domains (see Decisions). This lives in the panel
UI on the mail domains. The activity log records it.

**Self-service area** at `https://<mail host>/`: a mailbox signs in with its
address and password and can

- change its password (old password required; the panel's strength rules apply),
- see its storage use and limits,
- set out-of-office and filters (G2), delegations (G3), and sharing (G4b),
- create and revoke **app passwords**,
- download the setup for Apple devices and see the settings for mail programs.

There is **no password reset by e-mail**; the domain owner or an admin
resets it, as today.

**Sign-in and sessions** (in the mail service):
- A cookie `__Host-ownpanel_mailbox` on the mail host only, with its own sessions
  table in `mail.db`, CSRF protection like the panel, and a short idle timeout.
- The throttle is per address and per network: IPv4 per address, IPv6 per /48, as in
  `internal/mail/signin`. Self-service sign-ins have their own password-check
  slots, so a flood against mailboxes cannot slow IMAP or the panel's
  admin sign-in.
- **All sessions of a mailbox end** on a password change or reset, on conversion to shared,
  or when the mailbox is deleted. A password change offers "also revoke all app passwords".

**App passwords**:
- Generated, high-entropy tokens (`op_<id>_<secret>`), shown once,
  one per device.
- Stored as HMAC-SHA-256 with a server key, looked up by `<id>`: checking one costs a single fast hash, not N argon2id runs, and the
  timing reveals nothing about how many exist.
- Valid for IMAP, SMTP, DAV and ManageSieve, never for the self-service area.
- The sign-in cache key includes the credential id, and `signin.Valid` checks
  app passwords as well: a revoked app password stops working at once,
  including in open IMAP and DAV sessions.
- The last use (time and network) is shown per app password.
- Per mailbox option **"mail programs only with app passwords"**: the main
  password then works only in the self-service area. This is the prerequisite
  for a later two-factor sign-in there (TOTP), which the main password
  could otherwise bypass.
- The activity log records creation and revocation with the device name, never the token.
- No MCP tool ever creates or shows app passwords.

**Groundwork (built):** display names, organisations, app passwords and
"mail programs only with app passwords" exist in the panel and the mail
service. How it is built, where it is more precise than the plan above:

- **Configuration:** `Domain.Organisation` (an opaque id: `org-<random>`
  for a group, never the owner; empty for a domain that stands alone, which
  the mail service then treats as its own organisation `dom-<domain>`, so
  even a domain name of 253 characters fits), `Mailbox.DisplayName`, `Mailbox.AppPasswords []{ID, Name, MAC,
  Created}`, `Mailbox.AppPasswordsOnly`, `Mailbox.Revision` and
  `Config.AppPasswordKey`.
- **App password key:** one server key (32 random bytes), made by the panel
  once, kept sealed with `secret.key` and pushed in the configuration, which
  already carries secrets. The MAC is HMAC-SHA-256 under it of the secret
  together with the mailbox's storage id and the app password's id, so a MAC
  is worth nothing for another mailbox or id. The panel needs the key to
  create app passwords; the mail service will need it for the self-service
  area. Tokens are `op_` + 16 base32 characters of id + `_` + 32 of secret
  (160 bits); anything of that form is only ever checked as an app password,
  so the panel refuses main passwords of that form.
- **Revisions (Decision 6):** the counter belongs to the mail service. Each
  change a mailbox makes there gets `max(pushed revision, own revision) + 1`
  and is kept as an **overlay** per field (password, display name, the
  switch, app passwords added, app passwords revoked), each part with its own
  revision, in `mail.db`, written in the same transaction as the event. The
  panel applies an event only if its revision is higher than the mailbox's,
  stores that revision and pushes it; the mail service drops every part of
  the overlay the pushed revision reaches. Changes the panel makes itself do
  not touch the counter. If the panel changed the same field (password,
  display name or switch) itself at or after the moment of the mailbox's
  change, the panel's value wins (the activity log says so) and the overlay
  part is dropped by the next push; the panel keeps one such time per
  field, so an admin renaming a mailbox never undoes the mailbox's own
  password change. App passwords added or revoked always count.
- **Admin changes take effect at once:** before the panel saves a password
  reset, a display name or the switch, it records the mail service's
  pending events, so the push that follows carries a revision that
  includes the mailbox's own changes. The push also carries when the panel
  set each field (`Mailbox.PasswordSetAt`, `DisplayNameSetAt`,
  `AppPasswordsOnlySetAt`, Unix seconds); overlay parts keep the time of
  their event, and the mail service drops a part of the same field that is
  not newer, even before the panel saw the event. Both sides compare the
  same two times, so they agree on who won.
- **Events that cannot be recorded** (the panel's database failed) do not
  stop the others: such an event and every later account event of the same
  mailbox wait in the setting `mail_events_pending` and are tried again,
  in order, first on every round; events of other mailboxes and all other
  events go on. Until then the mail service keeps the mailbox's change.
- **Events:** `mailbox.password-changed`, `mailbox.display-name-changed`,
  `mailbox.app-passwords-only`, `mailbox.app-password-created`,
  `mailbox.app-password-revoked` (each with `Event.Account`: revision and the
  new hash, name, switch, app password with MAC, or revoked id) and
  `mailbox.app-password-used` (id and network, at most once an hour per app
  password, revision 0).
- **One mailbox never stops the push:** an app password the mail service
  reports for a mailbox that already has 50 (one was made in the panel at
  the same time) is not recorded, but its revision is, so the mail service
  drops it too; the activity log notes it. Before every push the panel
  leaves out, or cuts down, any domain, mailbox or alias the mail service
  would refuse (`config.DropInvalid`: app passwords beyond 50, invalid DKIM
  keys, invalid alias targets, a damaged password hash, …) and logs it, so
  the configuration of everyone else still loads.
- **Organisations** are owned by one panel user. An admin who groups
  domains of different owners does it with an organisation of their own or
  of one of the owners; the activity log notes "domains of different
  owners". A member sees an organisation an admin put their domain into only
  by name, and can take the domain out.

**Built (self-service area):** how it turned out, and where it differs
from the plan:

- **Package `internal/mail/account`** serves `/account/api/` on the web
  socket (wired in `cmd/ownpaneld/mail.go` through
  `server.Options.AccountHandler`): `POST`/`DELETE session`, `GET me`,
  `PUT password`, `PUT display-name`, `PUT app-passwords-only`,
  `POST app-passwords`, `DELETE app-passwords/{id}`, `GET apple-setup`
  (wiki/API.md). Changes go through `server.ApplyAccountChange`, so they
  are overlays with revisions and events as described above.
- **Sign-in:** `signin.Checker.CheckMain` on a Checker of its own (own
  tables, one hashing slot of its own, no cache): only the main password,
  `AppPasswordsOnly` ignored, anything of the form of an app password fails
  after the same work as a wrong password. Throttle as for mail programs:
  per (network, address) and per network, IPv4 per address, IPv6 per /48,
  fail closed when the tables are full. Clients whose address is unknown
  (no `X-Forwarded-For` from Caddy) share one bucket.
- **Sessions** in `mail.db` (migration 7, `account_sessions`): SHA-256 of a
  256-bit id, the storage id, the address and a fingerprint (SHA-256) of
  the password hash at sign-in; at most 20 per mailbox. Every request checks
  the session against the directory (storage id, address, password hash),
  so a reset by the panel, a password change on another device or a
  deleted mailbox end it at once; a conversion to shared will be added to
  that check with G3 (mailbox kinds do not exist yet). The session that
  changes the password gets the new fingerprint and stays; the mailbox's
  other sessions are deleted. Idle 30 minutes, at most 12 hours; ended
  sessions are pruned every 10 minutes.
- **CSRF:** changing requests need `X-Ownpanel-Request: 1`; a
  `Sec-Fetch-Site` other than `same-origin`, or an `Origin` that is not
  `https://` + the request's host, is refused; requests for another host
  than the mail host answer 404. 30 changes per session in 15 minutes.
- **Last use of app passwords** comes from the mail service's own
  `mailbox.app-password-used` events (kept 30 days), so the page shows
  uses of the last 30 days; the panel keeps the full history.
- **Shared code:** the Apple profile and the client settings moved to
  `internal/mailsetup`, the password rules to `passwd.CheckNew` and
  `config.CheckNewPassword`; the panel and the mail service use the same.
- **Not built:** organisation name on the page (the mail service only has
  the opaque id), a second sign-in step (TOTP), and a list of the
  mailbox's sessions.

**Known limits:** the new Outlook syncs IMAP through Microsoft's cloud, so
many people share a few Microsoft addresses. The per-network throttle counts
those addresses higher, and the docs mention it.

## G2 — Filters and out-of-office

**Where Sieve runs:** at **every local delivery**, which happens in two places:
port 25 (`smtpd`) and submission (mail between colleagues, so out-of-office also answers internal mail).
It runs once per recipient mailbox, after spam checks.
- `envelope :to` is the original recipient (an alias, if mail came through
  one). The mailbox's own address is available as the `:addresses` set and
  through `variables`.
- **Junk:** the implicit keep goes to Junk; an explicit `fileinto` can move
  mail out of it. `redirect` and `vacation` never run for mail judged as
  Junk or failing DMARC.
- Delivery into a shared mailbox runs that mailbox's script (G3; managed by
  holders of the Manage right).

**Extensions:** `fileinto`, `mailbox`, `envelope`, `imap4flags`, `body`
(size-limited), `copy`, `variables`, `relational`, `date` and `currentdate`
(RFC 5260, needed for out-of-office date ranges), `vacation` (RFC 5230),
`vacation-seconds`, `include` (RFC 6609; personal scripts only, `:global`
refused), `duplicate` (RFC 7352), and `spamtest` (RFC 5235, mapped to our
score). `regex` (an expired draft) is offered with a capped pattern length;
Go's RE2 runs in linear time. `subaddress` comes only once plus-addressing
exists. **`reject`/`ereject` are not offered** in v1: one reply to DATA
covers every recipient and there is no LMTP, so a per-recipient reject
would become a bounce sent after the fact (backscatter).

**Out-of-office** follows RFC 5230 and RFC 3834. It replies only when:
- one of the mailbox's own addresses (including aliases that deliver to it) appears in To or Cc;
- there is no `Auto-Submitted` header other than `no`;
- there is no `Precedence: bulk/list/junk` and no `List-*` header;
- the sender is not empty, `MAILER-DAEMON`, `owner-*`, `*-request` or `LISTSERV`, and not our own address;
- the message is not Junk and has no failed SPF or DKIM.

The reply goes to each sender at most once per period. Replies are tracked per (sender, handle) in `mail.db`, the minimum period is 1 day and the default 7. It uses the null sender,
`Auto-Submitted: auto-replied`, In-Reply-To and References, and a DKIM signature. `:from` must
pass the send-as check, and replies count against the sending limit.

**Redirect** is forwarding, with SRS and the domain's sending limit, at most
5 targets per script run, and only for mailboxes allowed to forward (a
domain-owner setting, on by default). Loop protection (RFC 5228 §2.10.3):
- our own `X-Ownpanel-Loop` header,
- a hop limit,
- no redirect to the mailbox's own addresses,
- `Delivered-To` checked at delivery, so two mailboxes that redirect to each other stop.

**ManageSieve** (RFC 5804) on port 4190, STARTTLS only (there is no
implicit-TLS variant), with the same sign-in as IMAP. The installer opens it
in the firewall and Mail health checks that it listens. The self-service area
has a **simple filter editor** and the **out-of-office form**. Both write a
marked section of the active script that advanced users can see.

**For staff who left:** a domain owner can set or clear out-of-office and forwarding for a
mailbox of their domain. The activity log records it, and the person sees
"set by …".

**Limits:** at most 10 scripts per mailbox, 64 KiB each; step and time
limits at runtime; scripts are parsed and checked when saved.

## G3 — Shared mailboxes, delegation, distribution groups

**Shared mailbox:** a mailbox of kind `shared`. It has no password and
cannot sign in. It receives mail and has a quota, folders and its own Sieve
script. The domain owner grants access to **personal mailboxes of the same
organisation**:

| Right | Effect | RFC 4314 letters |
|---|---|---|
| Read | see folders and messages; own read state | `lrs` |
| Write | flags, move, delete, create folders | `lrswitekx` |
| Manage | filters and out-of-office of the shared mailbox | — |
| Send as | From = the shared address (and its aliases) | — |
| Send on behalf | From = shared address, Sender = own address | — |

A personal mailbox can also **delegate itself** (an assistant reads the
boss's mail) from the self-service area, with the same rights.

**IMAP (the largest change):**
- **Sessions become multi-mailbox.** A resolver maps a folder name to
  (storage id, folder), and a session holds a watch per granted mailbox.
  Grants are re-checked on every config change: a revoked grant loses its
  selected shared folder immediately (`BYE` for that selection, or
  `NO [NOPERM]`).
- **Folder listing:**
  - Shared folders appear as `Shared/info@example.com/…` in a plain
    `LIST "" "*"` and are subscribed automatically, because iOS and Outlook
    ignore `NAMESPACE`. `NAMESPACE` announces `Shared/` for the programs that use it.
  - The person's view of shared folders **never carries `\Sent`,
    `\Drafts`, `\Trash` or `\Junk`**, so no program adopts them as the person's own.
- **Rights without the ACL extension:** go-imap v2's server dispatches a fixed
  set of commands, so `GETACL`/`MYRIGHTS` cannot be added without forking it.
  v1 therefore **does not announce `ACL`**. Read-only grants select folders
  `READ-ONLY`, and forbidden actions answer `NO [NOPERM]`. The ACL commands
  come later with a vendored, extended server, if programs need them.
- **Per-user `\Seen`** in shared mailboxes means each reader has their own read state; other flags are shared.
  It touches unread counts in SQL, SEARCH SEEN/UNSEEN, FETCH FLAGS, the
  implicit `\Seen` when a body is fetched, STORE, IDLE updates (another
  reader's `\Seen` is not broadcast), and `imap4flags` at delivery.
- **COPY/MOVE across mailboxes** (today only within one): quota is charged to the destination,
  and the Write right is needed on both sides.

**Sending (submission):**
- **Send as:** the shared address and its aliases are allowed as From and as MAIL FROM.
- **On behalf** is a **separate rule**, not an extension of "may send as". From may be the
  shared address only if `Sender:` is present and is the signed-in mailbox's own address.
  MAIL FROM is the shared address, so bounces reach the team.
- DKIM signs with the From domain, so DMARC stays aligned.
- Optional per shared mailbox: **keep a copy of sent mail** in its Sent folder, de-duplicated by
  Message-ID against copies the program already saved there.
- Limits count for the shared mailbox and for the sender.

**Distribution groups:** aliases with members, plus a **sender restriction**:
anyone, the organisation only, or members only (checked at port 25 with an
aligned SPF/DKIM pass, at submission against the signed-in mailbox). An option
**hides a group from the directory**.

**Configuration:** `Domain.Organisation`, `Mailbox.Kind`, `Mailbox.DisplayName`,
`Mailbox.Grants []{StorageID, Rights}`, `Mailbox.KeepSentCopy`,
`Alias.SenderPolicy`, `Alias.HideFromDirectory`. Validated: the same
organisation only, no grant to itself, no grant held by a shared mailbox.
MCP tools for kinds, grants and groups follow AGENTS.md.

**Converting:** a personal mailbox can become shared (its password and app passwords are dropped,
and its sessions end) and back (a password is set). Import (exists) works for shared mailboxes too.

## G4a — Calendars, contacts, directory

**Protocols:** CalDAV (RFC 4791) and CardDAV (RFC 6352) over WebDAV, plus sync
(RFC 6578) at `https://<mail host>/dav/`.

**Discovery** is mandatory, because Apple and DAVx5 look at the **address's**
domain, whose website is often hosted elsewhere:
- default records per mail domain: SRV `_caldavs._tcp` and `_carddavs._tcp` plus TXT `path=/dav/`;
- `/.well-known/caldav` and `/.well-known/carddav` answer PROPFIND with a 301;
- the principal exposes `current-user-principal`, `calendar-home-set`,
  `addressbook-home-set` and `calendar-user-address-set` (mailto: its own address and aliases);
- the Apple setup profile gains CalDAV and CardDAV payloads; it is the reliable route on Apple devices.

**Protocol surface we build ourselves**, whatever library is used for the XML:
- `sync-collection` with tokens;
- `getctag`;
- `current-user-privilege-set`, without which read-only shares look editable in Apple and then fail with 403;
- quota properties (RFC 4331);
- `calendar-query` with time ranges;
- VTIMEZONE and floating times.

**Storage:**
- Objects are files under the mailbox's data, like messages: written, fsynced, then indexed.
- `mail.db` holds the index: collection, UID, ETag, time range, recurrence end.
- Recurrence expansion is bounded by a horizon.
- Deletion records are kept 90 days for sync; an older token gets 403 `valid-sync-token`, which forces a full resync.
- **A restore starts a new sync epoch:** all tokens become invalid and ETags and ctags change, like UIDVALIDITY for mail.
- Default collections ("Calendar", "Contacts") are created **on first use**.
- The data counts towards the mailbox quota.

**Directory (global address list):** a read-only address book per
organisation in every mailbox's home. It lists the display names and
addresses of mailboxes, shared mailboxes and groups, except those hidden.
Programs use it for autocomplete.

**Limits:** object size (1 MiB per event, 1 MiB per vCard including photo),
100 collections per mailbox, 100 000 objects per collection, request size,
request rate per sign-in, and XML depth and size limits (untrusted input).

**Outlook:** no native CalDAV; the documented route is the free CalDav
Synchronizer add-on.

**Import/export:**
- `.ics` and `.vcf` upload and download per collection in the self-service area,
  with their own size limits.
- The whole-mailbox export gains `calendars/` and `contacts/`.
- Migration guides: Google via `.ics`/`.vcf` export or a CalDAV pull with an app password;
  Microsoft 365 via `.ics` and CSV export.

**Built (G4a):** how it turned out, and where it differs from the plan:

- **Packages:** `internal/mail/dav` (the HTTP handler: tree, sign-in,
  PROPFIND/PROPPATCH/REPORT/MKCALENDAR/MKCOL/GET/PUT/DELETE, the
  directory, import/export helpers), `internal/mail/davobj` (iCalendar and
  vCard: checks, index spans, filters, time zones, splitting and joining
  files) and `internal/mail/storage/dav.go` (collections, objects,
  deletions; migration 8). Hooks: `web.Options.DAV` and
  `server.Options.DAVHandler` (wired in `cmd/ownpaneld/mail.go`),
  `server.SignIn()`, `mailsetup.ClientSettings.DAVURL`/`WithDAV`, the panel's
  record list and three account API routes.
- **Tree:** `/dav/principals/<address>/`, `/dav/calendars/<address>/<calendar>/<object>`,
  `/dav/addressbooks/<address>/<book>/<object>`, the directory at
  `/dav/addressbooks/<address>/directory/` (a reserved name).
  `/.well-known/caldav|carddav` answer `GET`, `HEAD`, `PROPFIND` and
  `OPTIONS` with `301` to `/dav/`. `OPTIONS` needs no sign-in; the `DAV`
  header is `1, 3, access-control, calendar-access, addressbook,
  extended-mkcol` (no locking).
- **Sign-in:** HTTP Basic on every request through the mail programs'
  `signin.Checker` (`Check`: main password unless AppPasswordsOnly, or an
  app password; its cache makes repeated requests one HMAC). Requests for
  another host than the mail host answer 404. 3000 requests per mailbox in
  5 minutes, then `429` with `Retry-After`.
- **Authorisation:** `dav.Authorizer` (`Home`, `Collection` returning a
  privilege set) decides on every request; G4a's `OwnOnly` gives a mailbox
  all privileges on its own homes and nothing on others (404). G4b plugs a
  share-aware implementation in there. The directory is always read-only.
- **Storage:** files under `dav/<storage_id>/<collection id>/` in the mail
  data directory with the same durability as messages (temporary file,
  fsync, rename, fsync of the directory, then the index commit); replaced
  and deleted files go through `trash/` like messages. Tables
  `dav_collections` (seq, pruned_seq), `dav_objects` (UID, component,
  size, `start_at`/`end_at`, file, seq) and `dav_deletions`. Every change
  takes the collection's next sequence number. ETags, ctags and sync tokens
  are derived from the **DAV epoch** (`meta.dav_epoch`), the collection and
  that number, not stored: the start-up check sets a new epoch when it
  found rows without files or indexed stray files, or the `restored` marker
  exists, which changes all of them at once. Files without a row are
  indexed again when they are a valid object of an existing collection
  whose UID is not taken (`restored-<file>.ics`), otherwise removed.
  Deletions older than 90 days are pruned hourly (`dav.Handler.Run`); a
  token older than the kept deletions, of another collection or epoch, or
  not ours answers `403 valid-sync-token`.
- **Quota:** DAV bytes are added to `mailboxes.used_bytes` (not to the
  message count), so IMAP appends, delivery and the self-service page see
  them; a PUT that would pass the quota answers `507` with
  `quota-not-exceeded`. `quota-used-bytes`/`quota-available-bytes` on homes
  and collections.
- **Default collections** ("Calendar" `calendar`, "Contacts" `contacts`)
  are created on the first DAV or self-service request when the mailbox has
  no collection of that kind; deleting the last calendar brings a new
  empty one back on the next request.
- **Objects:** one VEVENT or VTODO (with its overrides, same UID) per
  calendar resource, or one vCard with a UID; VJOURNAL and others are
  refused (`supported-calendar-component`). Data is stored byte for byte,
  so the ETag of a PUT is valid. Limits as planned: 1 MiB per object (413
  with `max-resource-size`), 100 collections per mailbox and 100 000
  objects per collection (`507`), XML bodies 1 MiB / depth 32 / 20 000
  elements, content lines with more than 8 KiB of parameters refused.
- **Time ranges:** the index keeps the span of all occurrences; queries
  pre-filter on it and then evaluate the full filter. Recurrence is
  expanded with rrule-go up to 20 000 occurrences and **20 years** after
  the start (a rule that may go on is open-ended in the index and matches
  ranges beyond the horizon). TZIDs resolve as an IANA name (also at the
  end of Mozilla/Citadel ids), the VTIMEZONE's X-LIC-LOCATION, a list of
  common Windows names, the VTIMEZONE's standard offset as a fixed zone
  (approximate), else UTC; floating times, dates and approximated zones
  are widened by 14 hours in both directions, so no reader's zone misses
  them (clients filter again). The Go time zone database is built in.
- **Reports:** calendar-query (comp/prop/param filters, text-match,
  time-range), calendar-multiget, addressbook-query (with `limit`),
  addressbook-multiget, sync-collection (level 1; `limit` ignored),
  expand-property (answered like PROPFIND of the named properties, not
  expanded), principal-property-search and principal-search-property-set
  (empty until G5). `calendar-data`/`address-data` are returned whole:
  `<expand>`, `<limit-recurrence-set>` and partial retrieval are ignored.
- **PROPPATCH:** displayname, description and Apple's calendar-color are
  kept; Apple's calendar-order, calendar-timezone and
  schedule-calendar-transp are accepted and not kept; anything else fails
  the request (403/424). MKCALENDAR takes displayname, description, colour
  and the component set (VEVENT, VTODO); address books come with extended
  MKCOL (RFC 5689). COPY, MOVE and LOCK are not offered.
- **Directory:** vCard 3.0 cards of the organisation's mailboxes (FN and N
  from the display name) and aliases (not the catch-all, not addresses
  that are mailboxes), generated per configuration and cached; a field
  `HideFromDirectory` on mailboxes or aliases (G3) is honoured when present
  (looked up by name). Its ETags are hashes of the cards; its sync token
  is a hash of all of them: the current token gives no changes, any other
  one `403 valid-sync-token` (a full sync, which is small).
- **Discovery records:** SRV `_caldavs._tcp`/`_carddavs._tcp` `0 1 443
  <mail host>.` and TXT `path=/dav/` per mail domain, optional
  (recommended), listed and written with the others only when the mail
  host serves the web socket (`selfServiceProblem() == ""`).
- **Apple profile:** `com.apple.caldav.account` and
  `com.apple.carddav.account` payloads (host, 443, SSL, user name, principal
  URL, no password) when `ClientSettings.DAVURL` is set: always on the
  self-service page, in the panel when the mail host serves the web socket.
- **Thunderbird:** autoconfig cannot carry calendars; Thunderbird 115+
  finds them from the address or the mail host through the SRV/TXT records
  and `/.well-known` (documented in [[E-Mail|E-Mail]]).
- **Self-service:** `GET /account/api/collections`, `GET
  …/collections/{kind}/{name}/export`, `POST …/import` (10 MB, UID-based
  replace); the page lists calendars and address books with import and
  download and shows the settings for programs.
- **Export and backup:** the mailbox export zip gains
  `calendars/<name>.ics` and `contacts/<name>.vcf`, listed under `dav` in `manifest.json` (the zip import skips exactly those entries; mail folders of any name come
  back); the backup copies `dav/` with the rest of the data directory;
  deleting a mailbox deletes its collections.
- **Libraries (spike):** `emersion/go-webdav` was read and **not used**:
  its CalDAV/CardDAV servers own the request handling (fixed `Backend`
  interfaces, no sync-collection in the server, no privilege sets, no
  quota, PROPPATCH unimplemented, path layout and well-known redirect
  fixed, XML types internal), so using it would have meant working around
  most of it. The handler and the XML (encoding/xml with our own limits)
  are ours. Used: `emersion/go-ical` (parsing iCalendar, which pulls in
  `teambition/rrule-go` for RRULE expansion) and `emersion/go-vcard`
  (parsing vCards). Time zones, recurrence sets, filters and writing
  files are ours; go-ical's own time zone lookup (`time.LoadLocation` of
  the TZID only) and its encoder (no line folding) are not used.
- **Not built:** sharing (G4b), scheduling and free/busy (G5), COPY/MOVE,
  partial retrieval and server-side expansion, sync `limit`, a web
  calendar (G6).

## G4b — Sharing calendars and address books

A person shares a collection with mailboxes of the same organisation, read
or read-write, from the self-service area. Shared collections appear
**inside the receiver's home**, so every program shows them without special
support.
- `current-user-privilege-set` tells the truth for each receiver.
- The receiver's DELETE means "remove from my list", never "delete the owner's calendar".
- Writes by the receiver count against the **owner's** quota (507 when full).
- Shared mailboxes have a calendar and an address book too (team calendar, team contacts),
  governed by their grants.

## G5a — Free/busy and invitations to and from outside

- **Free/busy** within the organisation, through the scheduling outbox
  (what Apple and Thunderbird ask for when attendees are added).
- **Outgoing invitations:** when a saved event has attendees outside the
  organisation, the server sends iMIP (RFC 6047) REQUEST and CANCEL through
  the normal queue, with DKIM and limits.
- **Incoming replies:** an iMIP REPLY updates the organiser's event only
  when it is DMARC-aligned with the attendee's address and the UID matches.
- **Events from outside are never added automatically.** Incoming
  invitations stay mails, which programs offer to accept.

## G5b — Scheduling between mailboxes, rooms

- An internal schedule inbox (RFC 6638): invitations between mailboxes of
  the organisation land as "needs action".
- `SCHEDULE-AGENT` for programs that schedule themselves, and schedule-tag
  with `If-Schedule-Tag-Match`.
- Replies per occurrence, and SEQUENCE handling.
- **Rooms and resources:** a mailbox of kind `resource` whose calendar
  accepts when free and declines when busy (with conflict checks across
  recurrences), optionally only from listed people.

This phase is where most differences between programs live. It gets its own
design review with a client test matrix before it is built.

## G6 — Webmail, web calendar and contacts

Planned in [#30](https://github.com/danielgtmn/ownpanel/issues/30). It runs at
the mail host for self-service sessions and uses the mail service's account
API. It comes after G1–G4 so that it can show all of them; its backend API is
decided in its own design.

## Libraries

A spike decides before each phase:

- **Sieve:** `foxcpp/go-sieve` is, from memory, small, pre-1.0 and without
  `vacation`. Expect our own interpreter for the extension list above,
  with a parser fuzzed in tests.
- **DAV:** decided in G4a: our own handler and XML on `encoding/xml`;
  `emersion/go-webdav` was not a fit (see G4a, built).
- **iCalendar/vCard:** `emersion/go-ical` (with `teambition/rrule-go` for
  RRULE) and `emersion/go-vcard` for parsing (G4a).

## Permissions

| | Admin | Domain owner | Mailbox (self-service) |
|---|---|---|---|
| Group domains into organisations | yes (logged) | own domains | no |
| Make a mailbox shared / personal / resource | yes | own domains | no |
| Grant access to a shared mailbox, groups | yes | own domains, same organisation | no |
| Delegate own mailbox | — | — | yes, same organisation |
| Own password, display name, app passwords | reset only | reset only | yes |
| Filters, out-of-office | set/clear (logged) | set/clear for own domains (logged) | yes |
| Share own calendars / address books | — | — | yes, same organisation |
| Read calendars, contacts, filters of others | no | no | own and shared with them |

Admins and domain owners can reset a password and thereby read everything,
as with mail today. The self-service area and the docs say so.

## Known limits (documented)

- **iOS** has no IMAP push for servers other than Apple's and Exchange; it
  fetches every 15 minutes. DAV has no push either.
- **Signatures** stay in the mail programs; server-side signatures may come with webmail.
- **Outlook:** IMAP and CalDav Synchronizer; shared folders show poorly over IMAP.

## Not doing

- **ActiveSync (EAS), EWS, MAPI/HTTP:** huge, partly undocumented protocols, and
  ActiveSync needs a patent licence from Microsoft. Revisit only if many
  users need native Outlook.
- **Public folders** in Exchange's sense: shared mailboxes cover the use.
- **Chat, video, documents:** out of scope for a mail server.

## Open questions

1. Who may group domains into an organisation across **different owners**
   (a company whose domains belong to two members)? Plan: admins only,
   logged.
2. Should domain owners see which mailboxes have out-of-office on (for
   staff who left)? Plan: show "out-of-office on", not the text.
3. The self-service account API in the mail service vs. a later webmail API:
   design G1's API so that webmail can extend it.
