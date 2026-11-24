# Redirects

A redirect sends visitors from one address to another. ownpanel has two kinds:

| Kind | Example | Where it applies |
|---|---|---|
| **Domain redirect** | `www.example.com` → `https://example.com` | every request for a domain that **no site uses** |
| **Path redirect** | `/old.html` → `/new.html`, `/blog/*` → `https://blog.example.com` | one path, or everything below a prefix, on a **domain of one of your sites**; checked before the site's files |

Both are served by the web server: a domain redirect's domain gets a certificate like a site's domain, so `https://www.example.com` works once the domain points at the server. Requests over plain HTTP are first sent to HTTPS, then redirected.

## Fields

| Field | Meaning |
|---|---|
| `source_domain` | the domain the redirect applies to |
| `source_path` | empty for a domain redirect; otherwise an exact path like `/old` or a prefix like `/blog/*` (`/*` is every path) |
| `target` | a full address (`https://…` or `http://…`); path redirects may also give a path on the same domain (`/new`) |
| `code` | `301` (permanent), `308` (permanent, keeps the request method), `302` (temporary), `307` (temporary, keeps the method) |
| `keep_path` | append the requested path to the target. For a prefix, the part after it: `/blog/*` → `https://news.example.com` turns `/blog/a/b` into `https://news.example.com/a/b`. No effect for exact paths |
| `keep_query` | append the query string (`?a=1`) if the request has one |

### Examples

| Redirect | Request | Answer |
|---|---|---|
| `www.example.com` → `https://example.com`, keep path and query | `https://www.example.com/a?b=1` | `https://example.com/a?b=1` |
| `old.example.com` → `https://example.com/moved`, nothing kept | `https://old.example.com/x?y=1` | `https://example.com/moved` |
| `example.com/old` → `/new` | `/old?x=1` | `/new` |
| `example.com/blog/*` → `/news`, keep path | `/blog`, `/blog/`, `/blog/a/b` | `/news`, `/news/`, `/news/a/b` |

## Rules

- Domains are normalized (lower case, no trailing dot). IP addresses are not accepted.
- A domain redirect's domain must not be a domain of any site, the panel's own domain, or already redirect. A site cannot take a domain that redirects, and the panel domain cannot be set to one; remove the redirect first.
- To send a site's own domain elsewhere entirely, use a path redirect for `/*` on that site.
- A path redirect's domain must belong to a site you may change. Paths start with `/`; `*` is only allowed as `/*` at the end. Paths are matched exactly as written, including upper and lower case (like the site's files). Spaces, `?`, `#`, `%`, braces, `//` and `.`/`..` segments are not allowed in paths; enter non-ASCII characters as they are, not percent-encoded.
- An exact path is checked before prefixes, and longer prefixes before shorter ones.
- A target that leads straight back to where the redirect applies (same domain and a path the redirect matches) is refused, since it would redirect forever. Longer chains are not checked.
- A target with `?` or `#` cannot be combined with keeping the path or the query string.
- Each person can have at most 500 redirects (domain redirects they created plus path redirects of their sites).

The kept path is passed on decoded, like the web server's own `{path}`: `/blog/a%20b` becomes `…/a b`, which browsers encode again.

## Who may do what

- **Domain redirects** belong to the person who created them. They and admins can change and delete them. Tokens limited to some sites cannot see or manage domain redirects.
- **Path redirects** belong to their site (the owner shown is the site's owner). Whoever may change the site manages them, including tokens limited to that site.
- `read` tokens can list redirects but change nothing.
- Deleting a site deletes its path redirects; removing a domain from a site deletes the path redirects on that domain.
- A person who still owns domain redirects cannot be deleted.

## DNS

Domain status and "point to this server" work for the domain of a domain redirect too: `GET /api/dns/domain-status?domain=…` and `POST /api/dns/point`. See [[DNS|DNS]].

## Activity log

`redirect.create`, `redirect.update` and `redirect.delete` are recorded with the source and the target.

## API and MCP

Routes are listed in [[API|API]]; the MCP tools `list_redirects`, `set_redirect` and `delete_redirect` are in [[MCP|MCP]].
