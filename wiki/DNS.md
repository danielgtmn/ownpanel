# DNS

ownpanel can

- show whether the domains of a site or of a domain redirect point at the server,
- set them up with one click ("point to this server"),
- manage the DNS records of the domains (zones) you choose, from your accounts at **Hetzner**,
- add and delete whole domains (zones) at **Hetzner**, showing the nameservers to enter at the registrar.

Only Hetzner is supported. It uses the DNS part of the Hetzner Cloud API (`api.hetzner.cloud`), the DNS in the Hetzner Console. The old `dns.hetzner.com` API was shut down in May 2026 and is not supported; zones that still live only there are not visible.

## Server addresses

Start ownpaneld with the public addresses domains should point at, IPv4 and/or IPv6 ([[Configuration|Configuration]]):

```sh
ownpaneld -server-ip 203.0.113.5 -server-ip 2001:db8::5
ownpaneld -server-ip 203.0.113.5,2001:db8::5
```

They must be public addresses unless `-tls` is `internal` or `off` (the documentation ranges used above are only accepted in those modes; use your real addresses). Without them, domain status and "point to this server" answer that the server's public address is not configured. `GET /api/server` returns the configured list.

## Connecting Hetzner

1. In the Hetzner Console, open the **project** that holds your zones.
2. Go to **Security**, then **API tokens**, and create a token with **Read & Write** access.
3. In ownpanel, open **Settings**, section **Connected accounts**, and choose **Connect Hetzner**: a name (1 to 64 characters, unique among your connections) and the token. Every person sees Settings; members only see their connected accounts there.

Connecting an account only gives the panel access. **None of its domains shows up in the panel yet**: you choose them (next section).

A token covers exactly one project. For zones in several projects, add one connection per project.

The token is checked by listing the zones before anything is saved; a rejected token is not stored. A Read-only token is accepted, but Hetzner refuses every change, and the connection then shows that.

Adding, checking and removing connections needs a password sign-in or an access token with `write` access to all sites ([[Users and Access|Users-and-Access]]).

### What is stored, and how

- The token is stored in the database encrypted with **AES-256-GCM**. The key is in `<data>/secret.key`, 32 random bytes created on first start (mode 0600). The key is never in the database, so a copy of the database alone reveals nothing.
- The encryption is bound to its purpose: a value sealed for DNS tokens cannot be opened as anything else.
- The API never returns a token after it was added. Tokens do not appear in the activity log or in error messages.
- Without `secret.key` the stored tokens cannot be used; connections show an error and must be removed and added again.
- Backups include `secret.key` ([[Backup and Restore|Backup-and-Restore]]).

### Who sees what

- Connections belong to the person who added them. Admins see everyone's connections (name and owner, never the token) and can remove them, but can only read and change records through their own.
- Access tokens limited to some sites cannot use DNS connections, zones or records. They can see the domain status of their sites and use "point to this server" for them.
- `read` tokens can list zones and records.

## Which domains are in the panel

The DNS page and the sidebar list the domains that are **in the panel**:

- domains you chose to manage here (**DNS → Add domain**, or **Manage DNS here** on a domain's page),
- domains you added whose DNS is managed elsewhere (below), and
- every zone of your own connections that something of yours uses: a domain of your sites, of your whole-domain redirects or your mail domains, and for administrators the panel's own domain (a name below the zone counts too, like `shop.example.com` for `example.com`). Those are in the panel automatically and stay while they are used. Another person's site on a name in your zone does not bring it into your panel, but it still keeps the zone from being deleted at Hetzner (below).

**Add domain** offers three ways:

- **Choose one from your accounts**: a list of the zones of your connected accounts that are not in the panel yet, grouped by account when you have several. Choosing one changes nothing at Hetzner.
- **Create a new one** at Hetzner (below). A created domain is in the panel right away.
- **Use a domain whose DNS is managed elsewhere**: for a domain whose records you keep at another provider or the registrar. The panel only remembers the domain (a registrable domain like `example.com`, no subdomains or IP addresses; non-ASCII names in their `xn--` form), so it shows up in the sidebar as a domain of its own, ready for a site, a redirect or e-mail. Nothing is created or changed at any provider. This is also the only way while you have no connected account.

The page of a domain (**`#/d/<domain>`**) offers **Manage DNS here** when one of your accounts has its zone, or a link to create it when none has (or to connect an account when you have none). For a domain with nothing on it yet, it links to creating a site, a redirect or e-mail for it.

A domain you added whose DNS is managed elsewhere has **Remove from the panel** on its page. That removes only the entry; nothing changes at any provider, and sites, redirects and e-mail stay. While something of yours still uses a name in the domain, it stays in the sidebar through that use, and the panel says so. These entries are per person: nobody else sees yours, administrators included. Once you manage its DNS here instead (choosing its zone from your accounts, **Manage DNS here**, or creating it at Hetzner), the entry is removed on its own, so the domain is not listed twice.

**Stop managing here** on a zone's page takes a chosen domain out of the panel. The zone and its records stay at Hetzner unchanged, and you can add it again any time. A domain that something of yours uses cannot be taken out; the page says what uses it.

Upgrading from a version before this existed adopts nothing: zones that something of yours uses stay visible, all others disappear from the sidebar but stay available under **Add domain**. Nothing is deleted at Hetzner.

API: `GET /api/dns/zones` lists the zones in the panel, `GET /api/dns/zones/available` the others; `PUT /api/dns/connections/{id}/zones/{zone}/managed` starts managing a zone, `DELETE` on the same path stops (`409` while it is used). Both need what adding connections needs. Domains with DNS elsewhere: `GET /api/domains` lists yours, `POST /api/domains` with `{"name": "example.com"}` adds one, `DELETE /api/domains/{domain}` removes it ([[API|API]]).

## Adding and deleting domains at Hetzner

A domain (zone) can be created in one of your connections: `POST /api/dns/connections/{id}/zones` with `{"name": "example.com"}`.

- Only registrable domains are accepted, like `example.com` or `example.co.uk`; a subdomain such as `www.example.com` is part of its domain's zone. Non-ASCII names must be given in punycode (`xn--…`).
- Hetzner creates it as a primary zone with a default TTL of 3600 seconds, adds the SOA record and NS records of its nameservers, and assigns three nameservers. They are returned in `nameservers` (and listed with every zone): **enter them at the domain's registrar**, otherwise the records here are not used.
- A domain that already exists at Hetzner, in this or another project, is refused.

Deleting a domain removes the zone with all its records at Hetzner (on its page: **Delete domain**, with the name typed to confirm). This is separate from **Stop managing here**, which deletes nothing: `DELETE /api/dns/connections/{id}/zones/{zone}` with `{"confirm": "<zone name>"}`; the name must be repeated exactly.

- It is refused while any site, redirect, mail domain or the panel itself uses the domain or a name below it, whoever they belong to. The answer lists what uses it (things of other people without their names). Remove those first.
- A zone that is protected against deletion at Hetzner is refused; remove the protection in the Hetzner Console first.

Both work only through **your own** connections, with a password sign-in or an access token with `write` access to all sites.

## Managing records

Open a zone to see its record sets (all records of one name and type), with `@` (the zone itself) first. You can create, replace and delete record sets.

| Rule | Detail |
|---|---|
| Types you can change | `A`, `AAAA`, `CNAME`, `TXT`, `MX`, `CAA`, `SRV`, `NS` (the panel refuses to change other types; whether the record list shows them depends on what Hetzner returns) |
| TTL | 60 to 86400 seconds, or empty for the zone's default |
| Values | at least one, at most 50; duplicates are removed |
| Name | `@` for the zone, or a name relative to the zone; underscores are allowed (`_dmarc`), the first label may be `*`; non-ASCII names must be given in punycode (`xn--…`) |
| `A` / `AAAA` | an IPv4 / IPv6 address |
| `CNAME`, `NS` | a full host name such as `mail.example.com` |
| `MX` | `priority host`, like `10 mail.example.com` |
| `SRV` | `priority weight port target`, like `10 5 5060 sip.example.com` |
| `CAA` | `flags tag "value"`, like `0 issue "letsencrypt.org"` |
| `TXT` | plain text, up to 4000 characters, no line breaks; enter it without quotes |

Zones that are not primary zones at Hetzner (for example secondary zones) are marked read-only: their records cannot be listed or changed here. Record sets that Hetzner has protected ("locked") are refused on change; remove the protection at Hetzner first.

### Limits and refusals

- **SOA** records are managed by the provider and cannot be changed.
- **NS records at the zone apex** (`@`) are managed by the provider and cannot be changed. NS records at other names can (delegations).
- A **CNAME at the apex** is refused; use A and AAAA records.
- A **CNAME** can have only one value.
- Deleting a record set that does not exist is not an error.
- Zone lists are cached for 60 seconds, which keeps ownpanel well below Hetzner's limit of 3600 requests per hour per project. "Check" on a connection fetches the zone list afresh and shows the outcome in `last_error`.
- Writes that change nothing are not sent to Hetzner.

## Record templates

**Set up a service** on a domain's records page adds the records a common service needs in one go. Choose what to set up, fill in what the service gave you, and the panel shows what would change before anything is written:

| Template | Records |
|---|---|
| Confirm that you own this domain | a TXT record with the text a service gave you, at the domain or at a name you enter |
| Google Workspace mail | `MX 1 smtp.google.com`, SPF `include:_spf.google.com` ([Google: MX](https://knowledge.workspace.google.com/admin/domains/set-up-mx-records-for-google-workspace), [SPF](https://knowledge.workspace.google.com/admin/security/set-up-spf)) |
| Microsoft 365 mail | `MX 0 <your mail server>` (shown in the Microsoft 365 admin center under Domains), SPF `include:spf.protection.outlook.com`, `CNAME autodiscover → autodiscover.outlook.com` ([Microsoft](https://learn.microsoft.com/en-us/microsoft-365/admin/get-help-with-domains/create-dns-records-at-any-dns-hosting-provider)) |
| Fastmail mail | `MX 10 in1-smtp` / `20 in2-smtp.messagingengine.com`, SPF `include:spf.messagingengine.com`, the three DKIM CNAMEs `fm1`–`fm3._domainkey`, SRV for sending (465) and IMAP (993) ([Fastmail](https://www.fastmail.help/hc/en-us/articles/360060591153-Manual-DNS-configuration)) |
| E-mail on this server | no records here: it leads to the domain's e-mail page, which sets them up from the domain's e-mail settings ([[E-Mail|E-Mail]]) |

How the records meet what is there:

- **TXT values are added**, the other values at that name (other verification texts) are kept.
- **SPF:** a domain may have only one SPF value. If there is one, the template's `include:` is added right after `v=spf1` and everything else stays; the preview shows the old value as replaced. Two SPF values, a value that excludes the new sender, or one that would need more than 10 DNS lookups is reported as a conflict.
- **MX, SRV and CNAME** record sets are replaced as a whole; the preview shows the values they had. Replacing MX records moves the domain's mail to the new provider.
- A **CNAME** where the name has other records, or other records where the name has a CNAME, is reported as a conflict.
- Each record set is `add`, `change`, `ok` (already as it should be) or `conflict`. With any conflict nothing is written. Applying again when everything is `ok` writes nothing.

Every written record set is a `dns.record.set` entry in the activity log, plus one `dns.template.apply`. Read-only domains do not offer templates. The preview needs what listing records needs (a `read` token is enough); applying needs what changing records needs. API: `GET /api/dns/templates`, `POST /api/dns/connections/{id}/zones/{zone}/templates/{template}` ([[API|API]]). Through MCP: `list_dns_record_templates` and `apply_dns_record_template` ([[MCP|MCP]]).

## Domain status

Domain status works for every domain you control: the domains of sites you may see and the domains of your domain redirects ([[Redirects|Redirects]]). `GET /api/sites/{name}/domains/status` lists those of a site; `GET /api/dns/domain-status?domain=…` gives the same object for one domain.

For each domain, the status is one of:

| State | Meaning |
|---|---|
| `points_here` | at least one address was found and every one is a server address |
| `points_elsewhere` | at least one address is not a server address (those are listed) |
| `no_record` | the name does not exist or has no `A` or `AAAA` record |
| `lookup_failed` | the lookup failed for another reason; try again later |

**The status asks the zone's authoritative nameservers directly**, not a normal (recursive) resolver. A recursive resolver can remember "no such domain" for up to an hour (the zone's negative cache time), so a record you just created would keep showing "no record". Asking the zone's own nameservers avoids that. If no authoritative nameserver can be reached, the system resolver is used instead. Results are cached for 30 seconds; a lookup times out after 8 seconds. CNAME chains are followed.

If the zone of a domain is found among your connections, the status also tells which connection and zone "point to this server" would use. Otherwise the web interface offers **How to point it here**: the records to create where the domain's DNS is managed.

## Point to this server

"Point to this server" (`POST /api/sites/{name}/domains/{domain}/point`, or `POST /api/dns/point` with `{"domain": "…"}` for any domain you control, including a domain redirect's) writes the records for one domain:

- It finds the zone among **your own** connections: the most specific zone wins. If the same zone is in two of your connections, it refuses until you remove one.
- It writes an `A` record set with the IPv4 server addresses and an `AAAA` record set with the IPv6 ones, TTL 300. A record set that is already right is left alone, including its TTL.
- An address record set of a family the server has no address in is removed, because it would point elsewhere.
- It **never removes a CNAME**. If the name has one, it refuses and asks you to remove it.
- It refuses read-only zones, and when none of your connections has a zone for the domain.
- It returns what it changed (`{"changed": [...]}`; a removed record set has no values).

It needs `write` access to the site (or to be allowed to change the domain redirect), and the server addresses to be configured.

## Activity log

`dns.connect`, `dns.disconnect`, `dns.record.set`, `dns.record.delete`, `dns.point`, `dns.zone.create`, `dns.zone.delete`, `dns.zone.adopt` (managing a zone here), `dns.zone.release` (no longer) and `dns.template.apply` are recorded. Tokens never are, and TXT values longer than 60 characters are left out.

## Through MCP

`check_site_domains` checks a site's domains, `check_domain` one domain of a site or a whole-domain redirect, and `point_domain_at_server` points it at this server. `list_dns_connections` lists your connected accounts, `list_dns_domains` the domains in the panel (with `available`, the others of your connected accounts); `manage_dns_domain` and `stop_managing_dns_domain` choose them; `list_domains`, `add_domain` and `remove_domain` handle domains whose DNS is managed elsewhere. `list_dns_records`, `set_dns_record` and `delete_dns_record` read and change records. The same rules as in the web interface apply: only your own connections, and a `write` token for changes ([[MCP|MCP]]). Connecting accounts is done only in the web interface or the API ([[API|API]]), so a provider token never passes through an AI agent.
