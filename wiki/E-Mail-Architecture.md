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
                              mail.db      index, queue, block state,                         aliases, DKIM keys (encrypted)
                                           greylisting
                              srs.key      SRS key (0600)
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
| `POST /send` | Send a message the panel wrote itself (for example a password reset link): sender, recipient, subject, text and optional HTML. The service builds the message, signs it with the sender domain's DKIM key and delivers or queues it; the sender must be an address in one of the server's mail domains, and bounces are dropped. |

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
`queue/…`, `srs.key` (32 random bytes, 0600, created on first start; the
backup copies it with the rest, so SRS addresses stay valid after a
restore). Data is keyed by `storage_id`, so a reused row id or a restored
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
  redirect is refused without naming them, and so is a domain in which
  (or below which) another user's sign-in address lies, so their mail,
  such as a password reset link, cannot reach this member's mailboxes.
- An **admin** may enable mail for any domain.
- The other way round, a member cannot use another person's mail domain,
  nor its `autoconfig.`/`autodiscover.` names, as the domain of a site or
  redirect (admins may). A setup name that someone other than the mail
  domain's owner holds anyway (an admin's site, or a claim made before the
  mail domain) is still answered by the panel: Caddy matches the setup
  names before redirects and sites.

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
  most 21 days old (bounces of forwarded mail; only with the null sender,
  anything else to an SRS address gets `550 5.7.1`). Everything else is
  refused at once (no backscatter). The null sender `<>` is accepted.
- `postmaster@`/`abuse@` go to the alias of that name if one exists,
  otherwise to the domain's oldest mailbox. With no mailbox, and for
  `mail_host`, they go to the address in the setting `postmaster_address`
  (admin). An outside postmaster address is forwarded to through the queue
  like an alias (SRS sender in the domain, or in `mail_host`, the mail was
  addressed to; counted against that domain's hourly limit). Without a
  postmaster address, or when it is an address of a domain here that does
  not exist, they are refused with a temporary error and Mail health shows a
  warning.
- Over quota or low disk: `452`, temporary.
- Filtering, see [#28](#filtering--28).

## Mailboxes and clients — [#24](https://github.com/danielgtmn/ownpanel/issues/24)

- The panel shows the client settings: server `mail_host`, IMAP 993 (TLS),
  SMTP 465 (TLS) or 587 (STARTTLS), user name = full address.
- Automatic setup (`internal/panel/mailsetup.go`, `internal/api/mailsetup.go`):
  Thunderbird autoconfig at `autoconfig.<domain>/mail/config-v1.1.xml` and
  `<domain>/.well-known/autoconfig/mail/config-v1.1.xml` (sites of this
  panel), Outlook autodiscover (POX) at
  `autodiscover.<domain>/autodiscover/autodiscover.xml`, and an unsigned
  Apple profile (`com.apple.mail.managed`, no password) per mailbox for
  whoever may see the mailbox. The public answers exist only for active
  mail domains and are the same for every address of a domain. Caddy gets
  `autoconfig.`/`autodiscover.` hosts only once the panel saw the name
  resolve to the server's addresses only (CNAME to `mail_host` or A/AAAA):
  the background loop looks every 5 minutes, opening a domain's records
  looks at once, and a change re-syncs Caddy. A failed lookup changes
  nothing; a name that resolves elsewhere or not at all is dropped. So
  Caddy never asks a CA for a name that does not point here. A setup name
  that is a domain of the mail domain owner's own site or redirect is
  left to it.
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
  lock a person out); IPv6 counts per /48. When the throttle's tables are
  full, sign-ins from new IPs are refused until old entries expire (never
  let through uncounted). A successful sign-in is cached in memory for 15 minutes
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
duplicates.

How it is built (phase 1c):

- **Port 25** accepts addresses whose aliases only forward outside. The
  external targets of all recipients in one alias domain become one queued
  message; local mailboxes get their copy as usual.
- **Envelope sender:** the null sender stays null; a sender in one of our
  domains (or `mail_host`) is kept, and bounces go to its mailbox if it is
  one; everyone else gets `SRS0=<hash>=<tt>=<domain>=<local>@<alias domain>`.
  `tt` is the day (days since 1970 mod 1024) in two base32 characters as in
  the common SRS scheme; the hash is the first 40 bits of HMAC-SHA256 over
  `tt=domain=local` (lowercase), in lowercase base32 (8 characters). Hash and
  address are compared without case, because servers on the way may change
  it. SRS addresses are accepted for 21 days. An original sender that is
  itself an SRS address is wrapped again (no SRS1).
- **The message** keeps its original `From` and body; only header fields are
  prepended (`Received`, `Authentication-Results`, `X-Ownpanel-Score`), so
  the original DKIM signature still verifies. Forged
  `Authentication-Results` of our host name and incoming `X-Ownpanel-Score`
  fields are removed (see Filtering).
- **Not forwarded:** mail judged Junk (score or DMARC `quarantine`); the
  local copies go to Junk and an event `delivery.not-forwarded` names the
  alias and targets. The same holds for bounces to SRS addresses.
- **Limits** (deviation: an alias belongs to a domain, not a mailbox):
  forwarded mail counts once per message against the **alias domain's**
  `send_per_hour` (the same counter submission uses). When it is reached,
  the forward is dropped with a `delivery.not-forwarded` event if the
  message also reached a local mailbox, and refused with `451 4.7.1` if it
  had nowhere else to go. Bounces routed back to an SRS address count once
  per message against the SRS address's domain the same way. A message that
  is refused after all (a local mailbox could not store it) gives its count
  back, so the sender's retry is not counted twice.
- **Bounces of forwards:** when the outside server refuses a forwarded
  message for good, the queue sends a delivery status notification (from
  the null sender) to the original sender decoded from the SRS address.
- **Submission:** external targets of a local alias go into the outbound
  queue with the rest of the message; the sender is the signed-in mailbox,
  so there is no SRS, and the message counts once like any other.

## Sending — [#26](https://github.com/danielgtmn/ownpanel/issues/26)

- A mailbox may send as its own address or an alias that delivers to it
  (catch-all does not count), checked for the envelope and the header `From`.
  The message needs exactly one `From` field; several addresses in it need a
  `Sender` with one address, which the mailbox must also be allowed to send
  as. A header section over 1 MB is refused with `552 5.3.4`.
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
- Only public addresses are connected to: mail servers on loopback, private
  (RFC 1918, ULA), link-local, CGNAT, multicast or other special addresses
  are skipped, and a domain whose mail servers have only such addresses
  bounces at once (`5.4.4`). NAT64 addresses (`64:ff9b::/96`, and
  `64:ff9b:1::/48` in its /96 form only) count by the IPv4 address they
  carry. Otherwise anyone who can send mail could make
  the server open port 25 inside its own network. `-allow-private-mx` lifts
  this for test setups.
- On shutdown, an attempt that does not finish within the grace time is
  cancelled; recipients it already delivered or that were refused for good
  are saved as such, the others are tried again after the next start.
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
| CNAME (optional) | `autoconfig`, `autodiscover` | `<mail_host>.` | judged by where the name resolves (A/AAAA with the server's addresses are fine); a CNAME elsewhere is replaced, other records are left alone |

The mail host itself needs one record, checked by mail health
(`mail_host_spf`) and written by admins whose DNS connection holds its zone
(`POST /api/mail/host-spf`):

| Record | Name | Value | If something else exists |
|---|---|---|---|
| TXT (SPF) | `<mail_host>` | `v=spf1 a -all` | `a` inserted after `v=spf1`, same rules as the domain SPF |

Bounces and DSNs have the null sender, so receivers check SPF against the
HELO name (`mail_host`), and mail forwarded with SRS for addresses without
SPF pass uses `SRS0=…@<alias domain>`; postmaster@`mail_host` forwarding
uses the mail host as alias domain. Without this record those messages fail
SPF.

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
- Greylisting: off by default; when on, only for senders without an SPF
  pass or without PTR; triplets use the /24 (IPv4) or /64 (IPv6).
- Content score: missing `Message-ID`/`Date`, malformed headers, no PTR,
  display-name spoofing of the recipient's own domain.
- Dangerous attachments (executables, scripts, Office macros) are refused.
- Score above the Junk threshold: delivered to `Junk`. Per-mailbox Junk
  threshold and greylisting switch: *later* (one default for now).
- Virus scanning: *later*; it would need a scanner product this design avoids.

How it is built (phase 1c, `internal/mail/auth` and `internal/mail/filter`;
all DNS goes through one resolver interface, replaced in tests):

- **Order on port 25.** At `MAIL FROM` (once per connection) the client's
  IPv4 address is looked up in the blocklists; IPv6 addresses are not looked
  up yet. At `RCPT TO`: blocklist refusal, recipient, greylisting. At the end
  of `DATA`: SPF, DKIM, DMARC, attachments, score. Loopback and private
  client addresses skip blocklists and the PTR check.
- **SPF** checks `MAIL FROM` (the HELO name for the null sender) with
  `blitiri.com.ar/go/spf`. **DKIM** verifies at most 5 signatures.
  **DMARC** reads `_dmarc.<From domain>`, else `_dmarc.<organizational
  domain>` (public suffix list), whose `sp=` then applies; it passes when SPF
  (the HELO identity for the null sender, RFC 7489 3.1.2) or a DKIM
  signature passes for a domain aligned per `aspf`/`adkim` (relaxed: same
  organizational domain; strict: equal). A `pct` below 100 lowers the policy
  one step for all mail (reject → quarantine → none) instead of sampling.
  Several From addresses: each distinct domain (at most 5) is evaluated, and
  any `reject` failure refuses. Lookup errors give `temperror` and never a
  refusal (a DNS error on the way to a possible pass also gives
  `temperror`, not `fail`).
- **Answers:** DMARC `p=reject` failure `550 5.7.1` at `DATA`; a `From`
  field that cannot be read (so it would give no domain for DMARC and the
  own-domain check) `550 5.6.0`, except from loopback clients, where it
  only adds `bad-from`; SBL/CSS/XBL/
  DROP (`127.0.0.2`–`.7`, `.9` of a Spamhaus zone, including DQS zones under
  `spamhaus.net`) `554 5.7.1` for every recipient (the connection stays
  open; the text names the list); dangerous attachment `554 5.7.1`;
  greylisted `451 4.7.1`; header over 1 MB `552 5.3.4`.
- **Headers on delivery** (prepended in this order): `Return-Path` (local
  copies only), `Received`, one `Authentication-Results: <mail_host>;
  spf=…; dkim=…; dmarc=…`, and `X-Ownpanel-Score: 3.5 (no-ptr spf-softfail)`.
  Incoming `Authentication-Results` whose authserv-id is our host name and
  every incoming `X-Ownpanel-Score` are removed (anti-spoofing); other
  servers' results are kept.
- **Score points** (threshold `junk_score`, default 5; reaching it means
  Junk): `no-message-id` 1, `no-date`/`bad-date` 1, `no-from`/`bad-from`/
  `multiple-from` 2.5 each (`bad-from` only from loopback clients; others are
  refused), `no-ptr` 1.5 (only when the lookup answered),
  `spoofed-name` 3 (display name contains `@<recipient's domain>` while the
  From domain is not ours), `spf-softfail` 1, `spf-fail` 2.5, `dkim-fail`
  1.5 (a failing signature and none passing), `listed:<zone>` 3 per listing
  that does not refuse (PBL `127.0.0.10`/`.11`, other lists), `mime-broken`
  1 (structure could not be read; never a refusal on its own). DMARC
  `quarantine` sends to Junk whatever the score and adds `dmarc-quarantine`.
  Junk goes to the folder marked `\Junk` (INBOX if there is none).
- **Attachments:** the MIME structure is read with `go-message` (depth 5, 500
  parts); file names come from `Content-Disposition` `filename` and
  `Content-Type` `name` (RFC 2231 and encoded words decoded, a lenient
  fallback for malformed parameters). Refused extensions: `exe scr com bat
  cmd pif js jse vbs vbe wsf wsh ps1 msi msp jar lnk hta cpl reg scf iso img
  vhd vhdx docm dotm xlsm xltm xlam pptm potm ppam ppsm sldm` (trailing dots
  and spaces ignored). `.zip` attachments up to 10 MB are looked into one
  level (names only, nothing unpacked, at most 1000 entries); attached
  messages (`message/rfc822`) are walked like the rest.
- **Greylisting** (deviation: DKIM is not known yet at `RCPT`, so "aligned
  SPF/DKIM pass" became "SPF pass"): only for clients without reverse DNS
  or without an SPF pass. Triplet: network (/24, /64), envelope sender,
  recipient, in `mail.db`. The first attempt and retries within 5 minutes
  get `451 4.7.1`; a retry after 5 minutes (within 24 hours) passes, and
  the triplet then passes for 30 days after its last message. Triplets that
  never passed are pruned after 24 hours, passed ones 30 days after their
  last message (checked hourly). At most 1000 waiting triplets per network
  (/24, /48) and 200,000 in total; new ones beyond that are greylisted
  without being recorded. Seeing a triplet again writes only when its state
  changes.
- **Blocklist refusals** (answers in `127.255.255.0/24`) record the event
  `blocklist.refused` at most once a day per zone; the zone is then simply
  not used for that message.

### Configuration and events (phase 1c)

The panel pushes these in `config.Config` (`"filter"`); zero values mean the
defaults, so an older panel keeps working:

| Field | JSON | Default |
|---|---|---|
| `Filter.Blocklists` | `filter.blocklists` | `null` (missing) means `["zen.spamhaus.org"]`; `[]` turns blocklists off; at most 10 zones. A Spamhaus DQS key is given as the zone `<key>.zen.dq.spamhaus.net`. |
| `Filter.JunkScore` | `filter.junk_score` | `0` means 5.0 |
| `Filter.Greylisting` | `filter.greylisting` | `false` |

New event kinds: `blocklist.refused` (a blocklist refused this server's
queries; Mail health), `delivery.not-forwarded` (a message was not forwarded
outside: Junk, or the domain's limit). `delivery.bounced` is no longer used
for "forwarding not available".

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
- Free disk below 1 GB: incoming mail, APPEND, COPY and MOVE answer temporary
  failures (`NO [UNAVAILABLE]` in IMAP).

## Import and export

Moving to ownpanel means bringing old mail along; leaving means taking it
out. Both run **in the mail service**, because only it may read and write
mail data. The panel checks permissions, starts and cancels jobs, passes
files through without buffering and shows progress.

### Who may

| | Admin | Owner of the mail domain | Read-only or site-limited token |
|---|---|---|---|
| Import into a mailbox | yes | yes | no |
| Export a mailbox | yes | yes | no |
| See import jobs | yes | yes | yes (read) |

Export reads a whole mailbox without the mailbox user noticing, so it is a
write-level action, recorded in the activity log (visible to the domain
owner and admins), and limited to one export per mailbox at a time and four
in the service. The same is said in [[Security|Security]].

### Sources

1. **Another mail account over IMAP:** server name, port 993 (TLS) or 143
   (STARTTLS, required), user name, password. Works with providers that
   allow IMAP with a password or an app password: Gmail and Yahoo (app
   password; Gmail needs 2-step verification, Workspace admins can forbid
   it), IONOS, Strato, GMX, web.de, other panels, Dovecot. **Microsoft 365
   and Outlook.com are not supported** (they require an OAuth sign-in,
   planned later).
2. **A file upload:** `.mbox` (Google Takeout, Thunderbird, Apple Mail),
   `.eml`, or a `.zip` of those, including our own export. Google Takeout
   must be exported as `.zip` (not `.tgz`).

### Flow: check first, then copy

1. **Check:** for IMAP the service signs in, lists the folders and returns
   per folder the local target folder, message count and size
   (`STATUS`/`RFC822.SIZE`), and the total against the quota. For a file,
   the upload ends in the same overview after the file is scanned. Nothing
   is copied yet.
2. **Choose and start:** the person can untick folders and include Junk and
   Trash (off by default), then starts the job. For IMAP the password is
   sent again with the start request (the dialog keeps it in memory, the
   server never stores it).
3. **Progress** shows folders, messages copied, skipped as already there,
   failed, bytes; at the end a list of failed or skipped messages (folder,
   date, shortened subject, reason; at most 1000 entries).
4. **Cutover:** import, then point MX here, then run the same import
   again: it only fetches what is new (see repeats).

### Reading the source

- **IMAP stays read-only on the source:** folders are opened with
  `EXAMINE`, messages fetched with `BODY.PEEK[]`, so the old account's
  read state does not change. First `UID`, `FLAGS`, `INTERNALDATE`,
  `RFC822.SIZE` are fetched; messages over the message size limit are
  skipped without downloading. `\Deleted` messages are skipped.
- **The other server is untrusted:** its answers are read as they come
  (folder list at most 10 000 entries; per folder pass at most 2 000 000
  new messages, the rest follows with the next repeat; flags and sizes in
  batches of 500). Each step may receive a bounded number of bytes (16 MiB
  for answers that are not a message, the message size limit plus 1 MiB
  for a message), and the connection ends when nothing can be read or
  written for 5 minutes.
- Flags kept: `\Seen`, `\Answered`, `\Flagged`, `\Draft`, and keywords that
  are valid here (at most 50; `$Forwarded`, `$Junk`, `$NotJunk`, `$MDNSent`
  among them); invalid keywords are dropped, not the message.
- **Received date:** IMAP `INTERNALDATE`; for files the `From ` line
  (asctime, UTC), else the first `Received`, else `Date`, else now.
- **Line endings** are normalised to CRLF before storing, so size and quota
  count the stored bytes. Incoming `Authentication-Results` naming our own
  host are removed, as on delivery.
- **mbox dialects:** files are read as mboxo (no unquoting of `>From `)
  unless our export's `manifest.json` marks them as mboxrd. Flags come only
  from the first header block: `Status`/`X-Status`/`X-Keywords`,
  Thunderbird's `X-Mozilla-Status`/`X-Mozilla-Keys`. Thunderbird's `.sbd/`
  hierarchy and extensionless mbox files, and Apple Mail's
  `Name.mbox/mbox` bundles (other files in them ignored) are recognised.
- **Google Takeout** (`Takeout/Mail/All mail Including Spam and
  Trash.mbox`, one file, labels in `X-Gmail-Labels`): the `Takeout/Mail/`
  prefix is dropped; `Inbox` → INBOX, `Sent` → Sent, `Spam` → Junk, `Trash`
  → Trash, `Drafts` → Drafts, the first user label → that folder, otherwise
  Archive; `Unread` absent → `\Seen`, `Starred` → `\Flagged`. Large Gmail
  accounts are better moved over IMAP (no upload limit, repeatable).

### Folder mapping

- Special-use folders (RFC 6154) map to ours: `\Sent` → Sent, `\Drafts` →
  Drafts, `\Archive` → Archive, `\Junk` → Junk, `\Trash` → Trash. Without
  attributes, well-known names do too (`Sent Items`, `Gesendet`, `Papierkorb`,
  `Entwürfe`, `[Gmail]/Sent Mail`, …).
- Names: go-imap decodes modified UTF-7; the personal namespace prefix
  (e.g. `INBOX.`) is stripped; the remote delimiter becomes ours, a `/`
  inside a name component and `*`, `%` and control characters become `_`;
  a NIL delimiter means flat names; `\Noselect`/`\NonExistent` folders are
  not copied (their children are). Names are cut to 255 bytes, folders
  that exist are merged into, at most 1000 folders and depth 20 per job.
  The job records the remote → local mapping.
- **Gmail:** `\Flagged` ("Starred") and `\Important` are skipped (they
  only repeat other folders). `\All` ("All Mail") is copied **last into
  Archive**, skipping every message already imported anywhere in this
  mailbox; that is where archived mail lives in Gmail.

### Repeats and resume

- Every imported message records its key in `import_seen` (per mailbox,
  with the folder): SHA-256 of the normalised message (CRLF, without
  `Status`, `X-Status`, `X-Keywords`, `X-Mozilla-*`, `X-Gmail-Labels`). A
  message already recorded for that folder is skipped; for the Gmail
  `\All` pass, one recorded for any folder.
- IMAP jobs also keep a watermark per (host, user, remote folder,
  UIDVALIDITY): the highest UID copied. A repeat or a resumed job fetches
  only higher UIDs (when UIDVALIDITY changed, all again, deduplicated by
  key). This keeps repeats cheap and inside Gmail's download limit.
- A repeat does not update flags of messages copied before and does not
  bring back mail deleted here since.

### Jobs

- One import per mailbox at a time, at most two running in the service,
  more wait. A job is a row in `mail.db` (`imports`: kind, IMAP host, port
  and user, state `uploading`/`checking`/`ready`/`waiting`/`running`/
  `paused`/`done`/`failed`/`cancelled`/`interrupted`, counts, folder
  mapping, an error text, times), failures in `import_failures`,
  watermarks in `import_watermarks`. All reference the mailbox `ON DELETE
  CASCADE` (mail.db migration 5). Finished jobs are kept 30 days.
- **The IMAP password lives only in memory** for the running job; it is
  never written to `mail.db`, logs, events or errors, and is removed from
  any remote error text. After a restart (every update restarts the
  service) a running IMAP job becomes `interrupted`; **continuing asks for
  the password again** and resumes from the watermarks. A file job resumes
  by itself from its spool.
- **Gmail's download limit** (about 2.5 GB a day; answers like "bandwidth
  exceeded" or `[ALERT]`) pauses the job (`paused`) instead of failing it;
  it continues after the password is entered again, the next day.
- **Abuse:** the first failed sign-in ends the check or job (no retry); at
  most 10 checks and starts per mailbox and per person and hour. The
  service does not set the library's debug output (it would log LOGIN).
- **Deleting a mailbox** during a job: `DELETE /mailboxes/{sid}` cancels
  and waits for its jobs before deleting, and a job checks before every
  message that the mailbox is still configured, so it cannot bring
  deleted data back.
- **Limits:** before every message the job checks the quota and that free
  space stays above `MinFreeBytes`; either stops it with a clear error.
  Messages over the message size limit count as failed. Imported mail does
  not count as sending, is not filtered and is not forwarded.

### Uploads

- **Chunked and resumable:** the client declares name and total size
  (at most `MaxImportBytes`, 20 GiB) and gets an upload id, then sends
  parts of at most 256 MiB with `PUT …/upload/{id}?offset=`; the service
  appends each part to the spool and answers the bytes it has, so a broken
  connection resumes where it stopped. Every request stays inside the
  panel's existing upload size and time limits; Caddy needs no change.
- An upload is refused unless free space covers the declared size plus
  `MinFreeBytes`.
- Spools live in `imports/` (not `tmp/`, which the start-up check empties)
  and are deleted when the job ends, is cancelled, or has no row.
- **Zip files** (untrusted): at most 100 000 entries, 1000 folders; at most
  the message size limit per message entry and 100 GB unpacked in total
  (no limit on the compression ratio: the zip reader refuses entries larger
  than they declare, quota and free space stop the import, and folders of
  similar messages compress far better than 100:1); nested zips not
  opened; names in UTF-8 when the zip flag says so (or when they are valid
  UTF-8, as macOS writes them without the flag), else CP437; no `..`,
  absolute paths, NUL or backslash; duplicate or case-colliding names
  merged into one folder. Other entries are skipped and counted.
- **Reading files** holds little of them in memory: a header line is given
  up as soon as the header passes 1 MB, other lines are read in pieces of
  64 KiB, and only `From ` lines up to 512 bytes are looked at for a date.
  Cancelling stops a scan or job in the middle of a message.
- **Scans** of complete uploads run at most two at once (apart from the
  running jobs) and are scanned again after a restart, but only once more:
  an upload whose scan was started twice without ending (the service
  stopped while reading it, other than by a normal stop) fails with "the
  file could not be checked".

### Export

- **Download all mail** gives a `.zip`: `manifest.json` (format marker,
  per folder: name, file path, special-use, subscribed, message count;
  empty folders too) and one `mboxrd` file per folder with CRLF line
  endings. Paths are escaped (`..`, `.`, `:`, `\`, control characters,
  case collisions get a suffix); the manifest holds the real names.
- Messages are unchanged except for `Status:`/`X-Status:`/`X-Keywords:`
  lines at the top for flags (Thunderbird, Apple Mail, mutt and our
  importer read them); the `From ` line carries the received date.
  `\Deleted` messages are not exported. Each folder is a snapshot taken
  when the export reaches it; messages expunged meanwhile are skipped.
- Our zip imports back without loss: folders, special use, subscriptions,
  flags, dates.
  Its check says `own_export`; the interface then also selects Junk and
  Trash (they were cleaned up once already).

### Remote connections

The host name must be valid; it is resolved once, every address must be
**public** (shared helper with the queue: no loopback, private,
link-local, CGNAT, NAT64 of private, …), and the service dials **that
address** itself, then speaks TLS with `ServerName` = host name (no second
lookup, so no DNS rebinding). IPv6 is allowed here. Only ports 993 and 143;
certificates are verified (no opt-out); on 143 authentication only after
STARTTLS. Timeouts per command; the job may run for hours. When the
connection breaks during a job, it connects again (at most three times)
and goes on from the watermarks. Remote error
texts are shortened to one line of at most 200 characters.
`-allow-private-mx` allows private addresses here too (test networks).

### Streams through two hops

The admin-socket client's 30-second timeout does not apply to streams: a
second client without an overall timeout, using the request context and a
deadline that is pushed forward while bytes flow (idle timeout 2 minutes).
The admin server gets the same idle deadlines. The panel's export handler
pushes its write deadline forward per chunk instead of a fixed extension;
neither hop buffers. Caddy has no write limit; its read limit of one hour
per request fits the 256 MiB parts.

### Admin socket and panel API

```
POST   /mailboxes/{sid}/imports/check     body ImportSource (IMAP)  → ImportCheck
POST   /mailboxes/{sid}/imports           body ImportStart (IMAP source + choices) → ImportJob
POST   /mailboxes/{sid}/uploads           {name, size}              → ImportJob (uploading)
PUT    /mailboxes/{sid}/uploads/{id}?offset=  part                  → {received}
POST   /mailboxes/{sid}/imports/{id}/start  choices (file jobs; IMAP: + password to continue) → ImportJob
GET    /mailboxes/{sid}/imports           → []ImportJob, newest first
GET    /mailboxes/{sid}/imports/{id}      → ImportJob with failures
DELETE /mailboxes/{sid}/imports/{id}      cancel (a finished job: remove) → 204
GET    /mailboxes/{sid}/export            → application/zip stream
```

The panel mirrors these under `/api/mail/domains/{domain}/mailboxes/
{local_part}/…` with the permission rules above. Activity:
`mail.mailbox.import-started` and `mail.mailbox.exported` from the panel;
`import.finished`, `import.failed`, `import.paused`, `import.interrupted`
come as service events through `/events` (with counts, source host and
user, never the password) and are recorded like the others, so they appear
even if nobody watches. MCP: `list_mail_imports` and `cancel_mail_import`
only; starting an import (it takes another account's password) and
export are not offered to agents.

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
  `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`. Resource limits as
  a safety net (one process serves every mailbox): `GOMEMLIMIT=1280MiB`,
  `MemoryHigh=1536M`, `MemoryMax=2G` (the service is killed and restarts
  above it), `TasksMax=1024`. Normal use stays far below; change them with
  `systemctl edit ownpanel-mail` on servers with very many mailboxes.
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

Phases 1a, 1b and 1c are built.

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
