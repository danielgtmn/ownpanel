# DNS

ownpanel can

- show whether the domains of a site or of a domain redirect point at the server,
- set them up with one click ("point to this server"),
- add and delete whole domains (zones) at **Hetzner**, showing the nameservers to enter at the registrar,
- edit the DNS records of zones hosted at **Hetzner**.

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
3. In ownpanel, open **DNS** and add a connection: a name (1 to 64 characters, unique among your connections) and the token.

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

## Adding and deleting domains

A domain (zone) can be created in one of your connections: `POST /api/dns/connections/{id}/zones` with `{"name": "example.com"}`.

- Only registrable domains are accepted, like `example.com` or `example.co.uk`; a subdomain such as `www.example.com` is part of its domain's zone. Non-ASCII names must be given in punycode (`xn--…`).
- Hetzner creates it as a primary zone with a default TTL of 3600 seconds, adds the SOA record and NS records of its nameservers, and assigns three nameservers. They are returned in `nameservers` (and listed with every zone): **enter them at the domain's registrar**, otherwise the records here are not used.
- A domain that already exists at Hetzner, in this or another project, is refused.

Deleting a domain removes the zone with all its records: `DELETE /api/dns/connections/{id}/zones/{zone}` with `{"confirm": "<zone name>"}`; the name must be repeated exactly.

- It is refused while any site, redirect or the panel itself uses the domain or a name below it, whoever they belong to. The answer lists what uses it (things of other people without their names). Remove those first.
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

If the zone of a domain is found among your connections, the status also tells which connection and zone "point to this server" would use.

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

`dns.connect`, `dns.disconnect`, `dns.record.set`, `dns.record.delete`, `dns.point`, `dns.zone.create` and `dns.zone.delete` are recorded. Tokens never are, and TXT values longer than 60 characters are left out.

## Not available through MCP

The MCP tools only check domains (`check_site_domains`); managing DNS connections, domains and records is done in the web interface or the API ([[API|API]]).
