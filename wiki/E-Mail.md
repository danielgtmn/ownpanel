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
  and Archive. A storage limit per mailbox, a display name, and
  [app passwords](#app-passwords) for each device.
- **Organisations:** domains of one company grouped together, the boundary
  for sharing between mailboxes later on (see [Organisations](#organisations)).
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
changing their own password and app passwords (an area for them is
planned, see [[Groupware architecture|Groupware-Architecture]]), sharing
between mailboxes, reports from receivers about your mail, and
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
   | CNAME | `autoconfig` | `mail.example.com.` (recommended) |
   | CNAME | `autodiscover` | `mail.example.com.` (recommended) |

   The two recommended names let mail programs set themselves up from just
   the address and password; A/AAAA records with the server's addresses
   work as well. The panel serves them only after it has seen them point
   to this server (checked every few minutes, and whenever the records page
   is opened), so it never requests certificates for names that lead
   elsewhere.

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

Most programs find these settings by themselves: Thunderbird and others
ask `autoconfig.<domain>` (or `<domain>/.well-known/autoconfig/…` when the
domain is a site of this panel), Outlook asks `autodiscover.<domain>`, and
some use the SRV records. These answers exist only for active mail domains
and are the same for every address, so they never tell whether a mailbox
exists. For iPhone, iPad and Mac, **Download setup for Apple devices** on a
mailbox gives a profile to install; it contains no password and asks for it
during installation (it is not signed, so the device shows it as
unverified).
A mailbox can send as its own address and as forwarding addresses that
deliver to it (not as addresses the catch-all covers).

### Display name

**Display name** on a mailbox sets the name of the person or team behind
it, at most 64 characters (no line breaks and none of `<`, `>` and `"`).
The mail server will use it where it writes mail itself and in the address
book; mail programs still put their own name into the mail they send.

### App passwords

An app password is a separate password for one device or mail program
(for example "Anna's phone"). Mail programs sign in with the address and
the app password instead of the mailbox's password. Each one can be
revoked on its own: the device using it is signed out at once, everything
else keeps working.

- **App passwords** on a mailbox lists them with when each was created and
  last used, and from which network (the first three parts of an IPv4
  address, the first three groups of an IPv6 address; never the full
  address).
- **Create app password** asks for the device or program and shows the
  password **once**, with a button to copy it. It is not stored anywhere in
  readable form, so it cannot be shown again; create a new one if it is
  lost. Up to 50 per mailbox.
- **Revoke** ends it for good.
- **Only allow mail programs with app passwords:** when on, the mailbox's
  own password no longer works in mail programs (it still works on the
  mailbox's own page, see below). Create the app
  passwords first, or mail programs stop working. This is the setting to
  use if the mailbox password might be known to others, and it is the
  ground for a later second sign-in step.

App passwords are created and revoked in the web interface, on the
mailbox's own page or through the [[API|API]], never by an AI assistant
through MCP. Changing the mailbox's password does not revoke them, unless
the person asks for it on their own page.

### Your mailbox's own page

The people who use a mailbox manage it themselves at
**`https://<mail server name>/`** (for example `https://mail.example.com/`),
signed in with the full address and the mailbox password. There they can

- see how much room their mail takes and the storage limit;
- set their **display name**;
- **change their password** (the current one is needed; the same rules as
  in the panel: 10 to 200 characters), optionally signing out every mail
  program that uses an app password at the same time (all app passwords are
  revoked);
- **create and revoke app passwords** (shown once, with copy), see when each
  was last used in the last 30 days, and switch **Only allow mail programs
  with app passwords** on or off;
- read the **settings for mail programs** and download the **setup for
  Apple devices**.

Only the mailbox password signs in there, also when mail programs may only
use app passwords; app passwords never do. The page signs out after 30
minutes without use and after 12 hours in any case, and at once when the
password is changed elsewhere (by the person on another device, or by you
in the panel) or the mailbox is deleted. Too many wrong passwords from one
network hold back further tries for 15 minutes.

Changes made there show up in the panel and the activity log with the
mailbox as the one who made them (never with a password). The page says
that the people who manage the server can reset the password: there is no
"forgot password" by e-mail, and an administrator or the domain's owner can
set a new password at any time, which ends the person's sessions.

The page needs a mail server name of its own, other than the panel's
address (**Mail health** warns otherwise), and the mail service's web socket
(set up by `install.sh`).

### Organisations

By default every mail domain stands on its own. When several domains belong
to one company (for example `example.com` and `example.de`), group them on a
domain's page under **Organisation → Change**: choose an organisation of
yours or create one. Mailboxes can only share mail, calendars and contacts
with mailboxes of the same organisation, and only see those in the address
book, once those features exist; nothing is shared just by grouping.

- You can group only your own domains into your own organisations.
  Grouping domains that belong to different people is for administrators,
  and the activity log says so.
- **Let this domain stand alone** takes a domain out again; deleting an
  organisation lets all its domains stand alone.
- If an administrator put your domain into an organisation of someone
  else, the page says so; you can only take it out.

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
- the mail server name has an SPF record that allows this server
  (`v=spf1 a -all`, or `a` added to an existing one): bounces and mail
  forwarded to other servers are sent under that name. If its zone is in
  your DNS account, **Add the record** writes it,
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

## Moving mail in and out

Each mailbox has **Import mail** and **Download all mail**. Both are for the
owner of the mail domain and administrators, signed in with a password (or
with an unrestricted write token through the [[API|API]]), and both are
noted in the activity log.

### Bring mail from another account

1. Open the domain under **E-mail**, choose **Import mail** at the mailbox,
   then **From another mail account**.
2. Enter the user name (usually the full address), the password and the
   incoming mail server your provider names for mail programs (for well-known
   providers it is filled in from the address). Keep **Encrypted from the
   start (port 993)** unless the provider says otherwise.
3. **Check the account:** the panel signs in and lists the folders with the
   number of messages and their size, and whether they fit into the mailbox.
   Nothing is copied yet. Untick folders you don't want; junk and deleted
   mail are left out unless you switch them on.
4. **Start copying.** The import runs on the server; you can close the page.
   The mailbox shows its progress, what was copied, skipped as already there
   or not copied, and the messages that were left out with the reason.

The old account is only read: its messages stay unread there and nothing is
deleted. The password is used while copying and never saved, so an import
that was **paused** (the provider limits downloads per day; Gmail allows
about 2.5 GB a day) or **interrupted** (the panel was updated) asks for it
again under **Continue** and goes on where it stopped.

Providers:

- **Gmail:** needs an *app password*: turn on 2-step verification in your
  Google account, then create one under Security › App passwords and enter
  it instead of your password. Google Workspace administrators can forbid
  this. Server `imap.gmail.com`. Large Gmail accounts are better moved this
  way than with Takeout (no file size limit, can be repeated).
- **Yahoo:** also an app password (Account security), server
  `imap.mail.yahoo.com`. **iCloud:** an app-specific password from
  account.apple.com, server `imap.mail.me.com`.
- **IONOS, Strato, GMX, web.de, other panels:** the normal password usually
  works; some (GMX, web.de) need access by mail programs turned on in their
  settings first.
- **Microsoft 365 and Outlook.com are not supported yet:** they only allow a
  sign-in through Microsoft's own page. Export the mail as a file, or move it
  with a mail program that has both accounts.

### Bring mail from a file

Choose **From a file** and upload a `.mbox`, `.eml` or `.zip` of those (up to
20 GB). The file is sent in parts; if the connection breaks, the upload goes
on where it stopped. The panel then reads the file and shows the same folder
overview.

- **Google Takeout:** at takeout.google.com select only *Mail*, choose the
  **.zip** format (not .tgz), download it and upload the zip. Gmail labels
  become folders; mail without a label goes to Archive.
- **Thunderbird:** right-click a folder and export it (with the ImportExport
  add-on) as `.mbox`, or put the `Mail`/`ImapMail` folder of your profile into
  a `.zip`: subfolders are recognised.
- **Apple Mail:** Mailbox › Export Mailbox, then compress the exported
  folder into a `.zip`.
- A `.zip` from **Download all mail** of an ownpanel server brings back every
  folder with its read and flagged state, and includes Junk and Trash.

### Moving a domain here (cutover)

1. Turn on e-mail for the domain and create the mailboxes; leave the MX
   record pointing at the old provider for now.
2. Import each mailbox from the old account.
3. Switch the MX record to this server (see [Set it up](#set-it-up)) and set
   up the mail programs.
4. A day later, run the same import again: only mail that arrived at the old
   provider in between is copied; what is there already is skipped.

### Download all mail

**Download all mail** gives one `.zip` with a `.mbox` file per folder and a
list of the folders. Thunderbird (ImportExport add-on), Apple Mail and most
mail programs can open the `.mbox` files; ownpanel imports the zip back
without loss. One download per mailbox runs at a time.

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
mailbox; they and the domain's owner can also download all of its mail.
Mailbox owners should know this.

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
