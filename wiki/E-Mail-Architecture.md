# E-Mail architecture

This page records how ownpanel's e-mail service is built and why
([#22](https://github.com/danielgtmn/ownpanel/issues/22)). It is the plan the
code follows; items marked *later* are not built yet. How to use e-mail is on
[[E-Mail|E-Mail]].

## Decision in one paragraph

ownpanel implements its own mail server in Go. It does **not** run an
existing mail product (no Postfix, Dovecot, Rspamd, Stalwart, ClamAV, …) next
to the panel. It uses Go libraries where writing a wire format or a
well-specified algorithm again would only add risk:

| Library | Used for |
|---|---|
| `github.com/emersion/go-smtp` | SMTP server (incoming, submission) and client (delivery) |
| `github.com/emersion/go-imap/v2` (pinned beta) | IMAP protocol; ownpanel implements the session, storage and updates |
| `github.com/emersion/go-msgauth` | DKIM signing and verification, DMARC record parsing |
| `blitiri.com.ar/go/spf` | SPF evaluation |
| `golang.org/x/crypto/acme/autocert`, `golang.org/x/net/publicsuffix` | certificate for the mail host; organizational domain for DMARC (both already dependencies) |

Everything that decides what happens to mail (who may receive and send,
aliases, forwarding, queueing, retries, bounces, filtering, limits) is
ownpanel's code.

The mail server runs as **its own service**, `ownpanel-mail`, started from the
same binary (`ownpaneld mail`). The panel drives it the way it drives Caddy:
the panel keeps the desired state in SQLite and pushes one complete
configuration over a local admin socket.

## Why a separate service

- **Ports below 1024.** SMTP (25, 465, 587) and IMAP (993) need
  `CAP_NET_BIND_SERVICE`. The panel stays unprivileged and never gets it.
- **Exposure.** The mail service parses untrusted input from the whole
  internet. It runs as its own user, `ownpanel-mail`, with its own data
  directory, and cannot read the panel's database or `secret.key`. The panel
  cannot read stored mail; it only talks to the admin socket.
- **Availability.** Updating or restarting the panel does not stop mail. The
  mail service keeps the last configuration it was given and starts with it,
  like Caddy's `--resume`. Panel and mail failures are independent (see
  [sync](#sync)).

## Components

```
              internet                                        local only
  25 / 465 / 587 / 993 ──► ownpanel-mail ◄── /run/ownpanel-mail/admin.sock ── ownpaneld ◄── web, API, MCP
                            user ownpanel-mail                                 user ownpanel
                            /var/lib/ownpanel-mail/                            /var/lib/ownpanel/
                              config.json  last pushed config                    ownpanel.db  mail domains, mailboxes,
                              mail.db      index, queue, block state                          aliases, DKIM keys (encrypted)
                              messages/    one file per message                  mail-tls/    ACME account and certificates
                              queue/       outgoing messages
```

| Part | Where | Role |
|---|---|---|
| `internal/mail/config` | shared | The configuration the panel pushes, with validation used on both sides. |
| `internal/passwd` | shared | argon2id hashing and verification (moved out of `internal/panel`). |
| `internal/mail/server` | mail service | Admin socket, listeners, reloads, graceful shutdown. |
| `internal/mail/storage` | mail service | Mailboxes, folders, messages: SQLite index plus one immutable file per message. |
| `internal/mail/smtpd` | mail service | Port 25 (incoming) and 587/465 (submission), separate go-smtp servers. |
| `internal/mail/imapd` | mail service | IMAP on 993 (implicit TLS only). |
| `internal/mail/queue` | mail service | Outbound queue: delivery, retries, bounces. |
| `internal/mail/auth` | mail service | SPF, DKIM, DMARC verdicts; SRS; DKIM signing. |
| `internal/mail/filter` | mail service | Blocklists, greylisting, content score, attachment rules, Junk. |
| `internal/panel/mail*.go` | panel | Operations and **all permission checks**; builds the config; DNS records; health. |
| `internal/api/mail.go`, MCP tools, `web/src/modules/mail` | panel | Front doors only. |

### Admin socket

`/run/ownpanel-mail/admin.sock`: directory `ownpanel-mail:ownpanel-mail`
mode 0750, socket 0660; the user `ownpanel` is a member of the group
`ownpanel-mail` (the same scheme as Caddy's socket). Endpoints:

| Endpoint | Purpose |
|---|---|
| `POST /load` | Replace the configuration. Refused if invalid, or if a `storage_id` is already bound to another address. |
| `GET /status` | Config hash, listeners, certificate expiry, queue size, per-mailbox usage and block state. |
| `GET /events?after=<id>` | Events since a cursor: mailbox blocked, sign-in failures from many addresses, delivery problems. |
| `POST /mailboxes/{storage_id}/unblock` | Lift a sending block. |
| `DELETE /mailboxes/{storage_id}` | Delete a mailbox's data. |
| `POST /checks/outbound` | Try a connection to an outside port 25 (deliverability check). |

## Data

**Panel database** (next free migration number, e.g. `0011_mail.sql`):

- `mail_domains`: domain (unique in the whole panel), `owner_id` (no
  `ON DELETE` action: a user who still owns mail domains cannot be deleted),
  created.
- `mail_dkim_keys`: domain, selector, private key (encrypted with
  `secret.key`), state `active` / `pending` / `retired`, created. One active
  key per domain; rotation (*later*) adds a pending key that signs only after
  its record is visible.
- `mailboxes`: id, `storage_id` (random 128-bit, never reused), domain, local
  part, password hash, quota, send limits per hour/day, created.
- `mail_aliases`: id, domain, local part (`*` is the catch-all), targets (JSON
  list of addresses: mailboxes of the panel or external).
- settings: `mail_host`, the blocklist list and an optional Spamhaus DQS key
  (encrypted). `-mail-host` as a startup flag locks the setting, like
  `-panel-domain`.

**Mail service data** `/var/lib/ownpanel-mail` (`StateDirectory`, owner
`ownpanel-mail`, 0700): `config.json` (0600, rewritten only when it changed),
`mail.db` (SQLite WAL, one writer, `synchronous=FULL`), `messages/<storage_id>/…`,
`queue/…`. Data is keyed by `storage_id`, so a reused row id or a restored
older panel database can never expose another mailbox's mail. Data of a
mailbox missing from the configuration is kept and reported, never deleted by
a config load.

**Block state** lives in the mail service (`mail.db`), not in the pushed
configuration, so a full config push can never undo a block. The panel shows
what `/status` reports and unblocks through the admin socket.

**Durability:** before answering `250`, the message file and its directory
are fsynced and the index is committed with `synchronous=FULL`. The same for
the queue. At start the service reconciles index and files (rows without a
file are dropped, files without a row are indexed).

**Backups:** `ownpaneld backup` also copies `/var/lib/ownpanel-mail` into
the folder `mail/` of the archive (`-mail-data`, default the installed path;
`-no-mail` leaves it out because mail can be large). `mail.db` is copied
first, with `VACUUM INTO` from a read-only connection (root never creates
`-wal`/`-shm` files the service cannot write), then the files: every row of
the copy has its file. Restoring is the manual tar step of
[[Backup and Restore|Backup-and-Restore]] plus
`chown -R ownpanel-mail: /var/lib/ownpanel-mail`. A restored index is older
than the clients' caches, so the service changes `UIDVALIDITY` of every
folder when its start-up check finds a difference or the restore marker
file `restored` exists.

## Sync

A separate `mailSync` next to the Caddy `sync`, with its own mutex and
timeout. `Run` drives both; one failing does not block or fail the other. A
saved change whose mail push fails answers `NotSynced` with "saved, but the
mail server could not be updated yet". The panel only pushes when the hash of
the configuration differs from what `/status` reports. The same loop polls
`/events` and writes them to the activity log.

## Mail host and TLS

`mail_host` (e.g. `mail.example.com`) is the name of the server for mail: MX
target, the name in SMTP greetings, what clients connect to. It must not be a
site or redirect domain (refused like the panel domain); it may equal the
panel domain.

Caddy's certificates are private to Caddy, so the panel obtains one for
`mail_host` with ACME HTTP-01 (autocert, `HostPolicy` = only `mail_host`):

- The challenge route (`host = mail_host`, path
  `/.well-known/acme-challenge/*` → panel) goes into Caddy's single `:80`
  server: merged into the existing HTTP server, or a new one when none
  exists. `mail_host` never appears in a host matcher on `:443`, so Caddy does
  not request its own certificate for it. A `-tags caddy` test proves the
  challenge reaches the panel.
- autocert obtains an ECDSA and an RSA certificate (old servers without
  ECDSA). Both are pushed; the service presents them whatever the SNI, so
  clients without SNI work.
- The loop compares fingerprints and pushes again after a renewal.
- The panel asks for a certificate only once `mail_host` resolves to the
  server's addresses, and backs off exponentially after failures (the CA
  allows only a few failed validations per hour).
- If `mail_host` equals the panel domain, Caddy has a `:443` route for it
  anyway; that is fine, because Caddy answers only the challenge tokens it
  issued itself and passes others through. The `-tags caddy` test covers
  both cases.
- With `-tls internal` or `off` the panel makes a self-signed certificate.
- The ACME library always tries the TLS-ALPN challenge first; it fails
  because Caddy holds port 443, so every issuance costs one failed
  validation before HTTP-01 succeeds. Within the CA's limits, but a reason to
  replace autocert with a direct HTTP-01 client *later*.

The service only opens its ports once `mail_host`, a certificate and at
least one domain are configured.

## Ownership and permissions

Mail domains belong to a person. A domain has at most one mail domain in the
whole panel.

A mail domain is **active** only once its MX record, checked at the
authoritative nameservers, points to `mail_host`. Only active domains are in
the pushed configuration: the service receives for them, delivers to them
locally and lets their mailboxes send. This stops someone from adding a
domain they do not really own (say `gmail.com` on a site) and catching mail
other mailboxes of this server send there. The panel re-checks inactive
domains in the background and on request.

- A **member** may enable mail for a domain they control: a site or domain
  redirect they may see (`controlledDomain`), or a domain inside a zone of
  one of their own DNS connections. Site-limited tokens are refused by the
  mail operations themselves (`controlledDomain` alone would let them
  through). A domain used by another person's site or
  redirect is refused without naming them.
- An **admin** may enable mail for any domain.

| Operation | Owner (member) | Admin | Read-only token | Site-limited token | System |
|---|---|---|---|---|---|
| List domains, mailboxes, aliases, DNS check, health | own | all | yes | no | — |
| Enable / delete mail domain (delete needs `confirm`) | own (rules above) | any | no | no | no |
| Create / delete mailbox, alias | own domains | any | no | no | no |
| Set mailbox password | session or unrestricted write token | same | no | no | no |
| Change quota and send limits | lower only | yes | no | no | — |
| Unblock sending | no | yes | no | no | — |
| Read `mail_host` (client settings) | yes | yes | yes | no | — |
| Change `mail_host`, blocklists, DQS key, defaults | no | yes (read: admins) | no | no | — |
| Set up DNS records | own connections only | own connections only | no | no | no |
| Record events | — | — | — | — | yes |

Details of these rules:

- Once active, a domain stays active, even if its MX record changes later
  (a DNS hiccup must not stop mail). Inactive domains are looked at every
  5 minutes, and on request at most every 5 seconds.
- Admins may also enable mail for a domain another person's site uses; the
  per-person caps do not apply to them.
- `mail_host` cannot itself be a mail domain (its `postmaster@` goes to the
  postmaster address), and is refused for sites and redirects.
- Setting a mailbox password needs mail write access, which already means
  a password sign-in or a write token not limited to sites. There is no MCP
  tool that takes a mailbox password (nor one that creates a mailbox), so
  passwords never pass through an AI agent.
- The panel reaches the service through `-mail-admin
  unix:///run/ownpanel-mail/admin.sock`; without the flag mail data is only
  stored.

Password hashes and DKIM keys are never returned. An admin can read any
mailbox by setting its password; [[Security|Security]] says so. Mailbox users
have no panel account and cannot change their own password yet (*later*).

## Receiving (port 25) — [#23](https://github.com/danielgtmn/ownpanel/issues/23)

- STARTTLS offered; plain allowed (server-to-server). No AUTH on port 25.
- Own listener wrapper: connections per IP and in total; command and idle
  timeouts; message size limit (50 MB); at most 100 recipients.
- `RCPT TO` is accepted only for a mailbox, an alias or the catch-all of a
  configured domain, `postmaster@`/`abuse@` of every domain and of
  `mail_host`, and `SRS0=` addresses whose signature checks out and are at
  most 21 days old (bounces of forwarded mail). Everything else is refused at
  once (no backscatter). The null sender `<>` is accepted.
- `postmaster@`/`abuse@` go to the alias of that name if one exists,
  otherwise to the domain's oldest mailbox. With no mailbox, and for
  `mail_host`, they go to the address in the setting `postmaster_address`
  (admin); without it they are refused with a temporary error and Mail health
  shows a warning.
- Over quota or low disk: `452`, temporary.
- Filtering, see [#28](#filtering--28).

## Mailboxes and clients — [#24](https://github.com/danielgtmn/ownpanel/issues/24)

- The panel shows the client settings: server `mail_host`, IMAP 993 (TLS),
  SMTP 465 (TLS) or 587 (STARTTLS), user name = full address.
- IMAP (implicit TLS on 993 only, RFC 8314): `INBOX`, `Sent`, `Drafts`,
  `Junk`, `Trash`, `Archive` with special-use flags; IDLE, MOVE, UIDPLUS.
  Envelope and body structure are read from the message file on FETCH
  (storing them at delivery is *later*). LITERAL+ is not offered, so an
  oversized APPEND is refused before the client sends it. Quota is enforced on delivery and APPEND (`NO [OVERQUOTA]`) and
  shown in the panel; the IMAP QUOTA command is not offered (the library has
  none).
- Submission on 587 (STARTTLS required before AUTH) and 465 (TLS); AUTH
  PLAIN and LOGIN (go-sasl has no LOGIN server; a small one of our own).
- Sign-in: the IP throttle runs before any hashing; failures are limited per
  (IP, address) and per IP, never per address alone (that would let anyone
  lock a person out). A successful sign-in is cached in memory for 15 minutes
  under an HMAC of address, password and hash, so mail clients that open many
  connections do not cost an argon2id hash each. The service has its own
  hashing slots.
- JMAP, POP3, IMAP on 143, autoconfig: *later*.

## Aliases and forwarding — [#25](https://github.com/danielgtmn/ownpanel/issues/25)

Aliases deliver to mailboxes and/or external addresses; `*` is the catch-all.
Mail forwarded outside gets an SRS envelope sender
(`SRS0=<hash>=<tt>=<domain>=<local>@<alias domain>`, HMAC key in the service's
data); bounces to SRS addresses are routed back to the original sender. Mail
judged Junk is not forwarded. Alias expansion stops at depth 10 and removes
duplicates. External targets count against the alias owner's send limits.

## Sending — [#26](https://github.com/danielgtmn/ownpanel/issues/26)

- A mailbox may send as its own address or an alias that delivers to it
  (catch-all does not count), checked for the envelope and the header `From`.
- DKIM signature (RSA 2048) with the domain's active key.
- Queue on disk, delivery to MX hosts by preference (implicit MX fallback to
  A/AAAA, null MX `0 .` refuses at once), STARTTLS whenever offered; an
  unverifiable certificate is accepted (opportunistic TLS) unless MTA-STS
  applies; when the TLS handshake itself fails, one retry without TLS.
- Outbound MTA-STS (RFC 8461): *later*.
- Retries after 5 min, 15 min, 30 min, 1 h, 2 h, then every 4 h, at most 5
  days; then a bounce message (DSN) to the sender. Permanent failures bounce
  at once. No bounce for bounces.
- Source address: IPv4 unless the service runs with `-ipv6-outbound`
  (off by default; the panel turns it on *later* once the PTR check
  passes). IPv6 should only be used when `mail_host` has a matching AAAA
  record and the address has a PTR naming `mail_host` (otherwise SPF `mx`
  would fail); else IPv4.
- The MX lookup and port are replaceable, so tests can deliver to a local
  sink or a second instance.
- DANE: *later* (needs a validating resolver).

## DNS records — [#27](https://github.com/danielgtmn/ownpanel/issues/27)

For each mail domain the panel knows the records it needs and checks them at
the authoritative nameservers:

| Record | Name | Value | If something else exists |
|---|---|---|---|
| MX | `@` | `10 <mail_host>.` | replaced only after confirmation (mail would move here) |
| TXT (SPF) | `@` | `v=spf1 mx -all` | `mx` inserted after `v=spf1`, qualifier and `redirect=` kept; several SPF records, `+all`/`?all` or more than 10 lookups: reported, not changed |
| TXT (DKIM) | `<selector>._domainkey` | `v=DKIM1; k=rsa; p=…` | replaced (the selector is ours) |
| TXT (DMARC) | `_dmarc` | `v=DMARC1; p=none; adkim=r; aspf=r` | never changed |
| SRV | `_submissions._tcp`, `_imaps._tcp` | `0 1 465 <mail_host>.`, `0 1 993 <mail_host>.` | replaced |

Other TXT values at `@` are kept. When the domain's zone is in one of the
actor's own DNS connections with write access, "Set up records" first returns
the list of changes; changes to existing MX records need `confirm`. Otherwise
the panel lists exactly what to create, including how to split long TXT
values. MTA-STS policy hosting and TLS-RPT: *later*. DMARC `quarantine` is
suggested once DMARC reports are read (*later*).

## Filtering — [#28](https://github.com/danielgtmn/ownpanel/issues/28)

- SPF, DKIM, DMARC (alignment with the organizational domain): an
  `Authentication-Results` header on every message. A DMARC failure with
  `p=reject` is refused; with `p=quarantine` it goes to Junk.
- DNS blocklists (default `zen.spamhaus.org`): a listing adds to the score;
  SBL/XBL listings are refused. Answers in `127.255.255.0/24` mean the query
  was refused (common through public resolvers) and count as a lookup error,
  shown under Mail health. Optional Spamhaus DQS key.
- Greylisting: off by default; when on, only for senders without an aligned
  SPF/DKIM pass or without PTR; triplets use the /24 (IPv4) or /64 (IPv6).
- Content score: missing `Message-ID`/`Date`, malformed headers, no PTR,
  display-name spoofing of the recipient's own domain.
- Dangerous attachments (executables, scripts, Office macros) are refused.
- Score above the Junk threshold: delivered to `Junk`. Per-mailbox Junk
  threshold and greylisting switch: *later* (one default for now).
- Virus scanning: *later*; it would need a scanner product this design avoids.

## Deliverability — [#29](https://github.com/danielgtmn/ownpanel/issues/29)

"Mail health" in the panel: `mail_host` resolves to the server's addresses,
each address has a PTR naming `mail_host`, outgoing port 25 works (the
service tries), the server's IPv4 addresses are not listed on common
blocklists, the blocklist lookups themselves work, and the certificate is
valid, and nothing else holds port 25. A daily blocklist check with an
activity entry on a new listing is *later*. [[E-Mail|E-Mail]] explains what to do at the
hosting provider (unblock port 25, set PTR) and how to warm up a new address.
Receiving and showing DMARC reports: *later*.

## Limits and abuse — [#31](https://github.com/danielgtmn/ownpanel/issues/31)

- Per mailbox: messages per hour and per day (defaults 100/h, 500/day).
  One accepted message counts once, whatever the number of recipients;
  recipients per message are capped separately. The windows slide and are
  stored, so a restart does not reset them. Per domain: messages per hour (default 300).
  Admins set defaults and per-mailbox values; owners can only lower them.
- Per person: at most 20 mail domains and 200 mailboxes (fixed now; an admin
  setting is *later*).
- The service blocks sending for a mailbox that hits its hourly limit three
  times within a day or signs in successfully from more than 10 networks in
  an hour (IPv6 counted per /64, so phones are not mistaken for attackers);
  receiving goes on. The block is an event: activity log, a mark in
  the panel. Only admins unblock.
- Free disk below 1 GB: incoming mail and APPEND answer temporary failures.

## Webmail — [#30](https://github.com/danielgtmn/ownpanel/issues/30)

Not in the first version: people use mail clients. A webmail would be a
separate web app on top of the mail service; decided later.

## Install and operations

- `install.sh`/`update.sh` create the user and group `ownpanel-mail`, add
  `ownpanel` to the group, install `ownpanel-mail.service` and open 25, 465,
  587 and 993 in ufw. `install.sh` warns if something else listens on port 25
  (exim, postfix); updates never stop for it. The service reports a busy port
  in `/status`, shown under Mail health.
- Unit: `User=ownpanel-mail`, `StateDirectory=ownpanel-mail`,
  `RuntimeDirectory=ownpanel-mail` (0750), `UMask=0077`,
  `AmbientCapabilities=CAP_NET_BIND_SERVICE`,
  `CapabilityBoundingSet=CAP_NET_BIND_SERVICE`, `NoNewPrivileges`,
  `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`.
- Every binary update restarts the service. On stop it refuses new
  connections with `421`, finishes running transactions (bounded), then exits.
- Development: `scripts/dev.sh` starts the service with high ports (2525,
  4465, 5587, 9993) and `-tls internal`.

## Scaling

One server, SQLite and files: a few hundred mailboxes. Several servers are
[#7](https://github.com/danielgtmn/ownpanel/issues/7).

## Phases

Each phase is a pull request that is tested end to end. The sections above
describe the result after phase 1c; items marked *later* come after it.

1. **1a — receive and read:** the service, admin socket, sync, `mail_host`,
   certificate, domains (with MX activation), mailboxes (API, MCP, UI), port
   25, storage, IMAP, install/update, backup.
2. **1b — send:** submission, queue, DKIM, sender checks, limits and blocks,
   bounces, DNS record check and setup, Mail health.
3. **1c — check and forward:** SPF/DKIM/DMARC verdicts, blocklists, content
   score, attachment rules, Junk, optional greylisting, aliases, catch-all,
   forwarding with SRS.
4. ***Later:*** outbound MTA-STS, DANE, DKIM rotation, daily blocklist
   monitor, DMARC reports, admin settings for per-person caps, per-mailbox
   filter settings, mailbox users changing their own password, webmail,
   JMAP/POP3, virus scanning.
