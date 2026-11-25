# E-Mail

ownpanel runs its own mail server: it receives mail for your domains, keeps
it in mailboxes that mail programs read, and sends mail directly to other
servers. Nothing is sent through an outside sending service. How it is built
and why is on [[E-Mail architecture|E-Mail-Architecture]].

Deliverability, not the server, is the hard part of running mail yourself:
whether other providers accept your mail depends on your server's address,
its reverse name and your DNS records. Read [Before you start](#before-you-start).

## What you get

- **Mailboxes** with a password, used with any mail program (Thunderbird,
  Apple Mail, Outlook, phones). Folders for Inbox, Sent, Drafts, Junk, Trash
  and Archive. A storage limit per mailbox.
- **Forwarding:** extra addresses that deliver to mailboxes or to outside
  addresses, and an address that catches everything else on a domain.
- **Signed outgoing mail** with a key per domain, so receivers can tell the
  mail really comes from your domain.
- **DNS records** for mail checked, and set up with one click when the
  domain's DNS is at a connected provider (see [[DNS|DNS]]).
- **Spam checks** on incoming mail: senders' signatures and policies,
  blocklists, a simple score and a Junk folder. Dangerous attachments
  (programs, scripts, documents with macros) are refused.
- **Limits** on how much each mailbox and domain may send. A mailbox that
  behaves as if its password was stolen is stopped from sending until an
  administrator allows it again.
- **Mail health** in the settings: shows what stands between your server and
  good delivery.

Not there yet: a webmail in the browser, virus scanning, mailbox users
changing their own password, reports from receivers about your mail, and
outgoing encryption policies (MTA-STS, DANE). See [[Roadmap|Roadmap]].

## Before you start

1. **Port 25 must be open in both directions.** Many hosting providers block
   outgoing port 25 on new servers (Hetzner, AWS, Google Cloud, Azure, many
   others) and unblock it on request. Ask before you rely on sending.
   Incoming 25, 465, 587 and 993 must be reachable; the installer opens them
   in ufw.
2. **A mail server name.** Pick a name such as `mail.example.com` and point it
   at the server (A and, if the server has IPv6, AAAA record). Mail
   programs connect to it and other servers deliver to it.
3. **Reverse name (PTR).** At your hosting provider, set the reverse name of
   the server's IPv4 (and IPv6) address to the mail server name. Many
   receivers refuse mail from addresses without a matching reverse name.
   This can only be set at the provider, not in DNS.
4. **Nothing else on port 25.** Some systems come with a small mail program
   (exim, postfix) for messages from cron. The installer warns about it;
   stop it, e.g. `systemctl disable --now exim4`.

## Set it up

1. **Settings → E-mail:** enter the mail server name and a postmaster
   address (an address of yours that receives reports about mail problems).
   The server's certificate is requested automatically once the name points
   at the server. Until the mail server has a name, a domain's e-mail page
   only says so and shows neither records nor settings for mail programs.
2. **Turn on e-mail for a domain:** from the domain in the sidebar, or from
   the add menu. You can do this for a domain of one of your sites or
   redirects, or one in a zone of your DNS connection. Administrators can do
   it for any domain.
3. **DNS records:** the domain's e-mail page lists the records mail needs.
   If the domain's DNS is in one of your DNS connections, **Set up records**
   creates them; if the domain currently receives mail somewhere else, you
   confirm first that it should come here from now on. Otherwise create them
   where you manage the domain's DNS:

   | Record | Name | Value |
   |---|---|---|
   | MX | `@` | `10 mail.example.com.` |
   | TXT | `@` | `v=spf1 mx -all` (or your existing one with `mx` added) |
   | TXT | `<selector>._domainkey` | shown on the page |
   | TXT | `_dmarc` | `v=DMARC1; p=none; adkim=r; aspf=r` (only if you have none) |
   | SRV | `_submissions._tcp` | `0 1 465 mail.example.com.` |
   | SRV | `_imaps._tcp` | `0 1 993 mail.example.com.` |

4. **Wait for the domain to become active.** The domain receives mail once
   its MX record points at the mail server name; the panel checks every few
   minutes, or at once with **Check now**. Until then its mailboxes can
   neither receive nor send. This protects other people's mail: nobody can
   claim a domain on this server that does not really point here.
   Administrators can make a domain receive at once (**Receive mail here
   now**), for setups where mail reaches the server another way.
5. **Create mailboxes** and forwarding on the domain's e-mail page.

## Mail programs

| | |
|---|---|
| Server (incoming and outgoing) | the mail server name, e.g. `mail.example.com` |
| User name | the full e-mail address |
| Password | the mailbox's password |
| Incoming (IMAP) | port 993, SSL/TLS |
| Outgoing (SMTP) | port 465, SSL/TLS, or port 587, STARTTLS |

Many programs find these settings by themselves from the SRV records.
A mailbox can send as its own address and as forwarding addresses that
deliver to it (not as addresses the catch-all covers).

## Forwarding

A forwarding address delivers to one or more mailboxes of this server or
to outside addresses. **All other addresses** catches mail to every address
of the domain that has no mailbox or forwarding of its own.

Mail forwarded to an outside address keeps its original sender in the
message, but the server uses a rewritten return address so that the
receiving server still accepts it; bounces come back and reach the original
sender. Mail judged as spam is not forwarded.

`postmaster@` and `abuse@` of every domain always receive mail: through a
forwarding address of that name if there is one, else into the domain's
first mailbox, else to the postmaster address from the settings. The
postmaster address may be outside (at another provider): then that mail is
forwarded to it like a forwarding address.

## Limits

| | Default |
|---|---|
| Storage per mailbox | 5 GB |
| Messages per mailbox | 100 per hour, 500 per day |
| Messages per domain | 300 per hour |
| Size of a message | 50 MB |
| Recipients of a message | 100 |
| Mail domains / mailboxes per member | 20 / 200 |

Administrators can change the limits of a mailbox; owners can only lower
them. One message counts once, whatever the number of recipients.

When a mailbox hits its hourly limit three times in a day, or signs in from
more than ten different networks in an hour, the mail server stops it from
sending (receiving goes on) and the activity log says so. Only an
administrator can allow it to send again, from the mailbox list. Change its
password first if it may have been stolen.

## Mail health

**Settings → E-mail → Mail health** checks:

- the mail service runs with the current settings,
- the mail server name points at this server,
- each address of the server has the mail server name as reverse name,
- connections to port 25 of other mail servers get through,
- the certificate is valid,
- every port is listening (another mail program may hold one),
- postmaster mail has somewhere to go,
- no blocklist refused to answer this server in the last 7 days.

## Spam checks

**Settings → E-mail → Spam checks** (administrators):

- **Lists of known spam senders:** by default `zen.spamhaus.org`. Mail from
  a server Spamhaus lists as a spam source is refused; a listing on any
  other list adds points towards Junk. You can use other lists or none. Spamhaus
  answers queries from large public DNS resolvers with a refusal; Mail
  health shows it when that happens. With a free Spamhaus key, enter
  `<key>.zen.dq.spamhaus.net`: the key is stored encrypted and only shown
  masked.
- **Points for Junk** (1–50, default 5): every sign of spam adds points
  (failed sender checks, suspicious content). Mail with at least this many
  points goes to the Junk folder and is not forwarded to outside addresses.
- **Hold back first mail from unknown servers** (off by default): the first
  message from a server that cannot show it may send for its domain is
  accepted only when it tries again. Real mail servers do, so such mail
  arrives some minutes later; much spam never comes back.

Mail that claims to come from one of your own domains but was not sent by
its servers (no passing SPF or signature) is refused.

## New server address: warm it up

Large providers judge new addresses by their history. For the first weeks,
send moderate amounts of mail that people expect and answer, check the
blocklists the mail health page mentions, and keep the DMARC record at
`p=none` until everything is signed correctly. Raise it to `p=quarantine`
later.

## Where mail is stored

In `/var/lib/ownpanel-mail`, owned by the mail service's own user
`ownpanel-mail`; the panel itself cannot read it. Backups include it unless
made with `-no-mail` (see [[Backup and Restore|Backup-and-Restore]]).
Deleting a mailbox or turning off e-mail for a domain deletes its stored
mail.

Administrators can set any mailbox's password, and with it read the
mailbox. Mailbox owners should know this.

## Troubleshooting

- **The domain stays "waiting":** its MX record does not name the mail server
  name yet, or the change has not reached its nameservers. The page shows
  what was found.
- **Mail to Gmail or Outlook lands in spam or is refused:** check Mail health
  (reverse name, blocklists), that all records show as set up, and that
  outgoing port 25 is unblocked.
- **Mail programs cannot connect:** check that 993, 465 and 587 are open in
  the firewall and that the certificate check in Mail health is ok.
- **Logs:** `journalctl -u ownpanel-mail`. Logs never contain message
  contents or passwords.
