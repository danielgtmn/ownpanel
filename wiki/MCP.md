# MCP

ownpaneld speaks the Model Context Protocol, so AI assistants can manage sites: create them, publish files, go back to an earlier version. The tools call the same operations as the API and are subject to the same permission checks.

## Endpoint and authentication

- **Endpoint:** `/mcp` on the panel's address, using streamable HTTP in stateless mode (every request carries its own credentials; there is no MCP session to remember).
- **Authentication:** `Authorization: Bearer op_…` with an access token. **Only access tokens work.** The browser session cookie is never accepted, so a signed-in browser cannot use MCP. A missing, invalid or expired token gets `401` with a `WWW-Authenticate` header.
- **Rights:** the token's rights apply. A `read` token can only look; a token limited to some sites only sees and publishes those; and it never has more rights than its user has. See [[Users and Access|Users-and-Access]].
- **Reachability:** with `-panel-domain panel.example.com`, use `https://panel.example.com/mcp`. Without it, `/mcp` is only reachable on the daemon's own address (`127.0.0.1:8600`; the Host must be local), for example through an SSH tunnel.
- Browsers' cross-site requests are refused; clients that are not browsers send no `Origin` header and are unaffected.
- Requests are limited to 64 MB.
- Activity log entries show the token that was used.

Create a token under your account in the web interface (password sign-in is needed to create tokens).

## Connect Claude Code

```sh
claude mcp add --transport http ownpanel https://<panel-domain>/mcp \
  --header "Authorization: Bearer op_…"
```

Other clients need the same two things: the address and the header `Authorization: Bearer op_…`.

## Tools

Version ids come from `get_site`; a version id starts with a UTC timestamp. Errors are returned as tool errors with a readable sentence.

| Tool | Arguments | What it does |
|---|---|---|
| `list_sites` | none | Lists the sites you can manage, sorted by name. For each: name, domains, owner, id of the live version, all kept versions (newest first, each with `id`, `created_at`, `live`). |
| `get_site` | `site` | Shows one site in the same form. |
| `create_site` | `name`, `domains` (optional list) | Creates a new, empty site you own. Fails if the name or a domain is taken. Nothing is served until something is published. Needs a `write` token that is not limited to sites. |
| `set_site_domains` | `name`, `domains` | Replaces the whole domain list of an existing site. Domains not listed stop being served. Safe to repeat. |
| `delete_site` | `name`, `confirm_name` | Permanently deletes the site with all its files and versions. `confirm_name` must equal `name`, otherwise nothing happens. Needs a `write` token that is not limited to sites. |
| `publish_files` | `site`, `files` | Publishes files given directly as a new live version, replacing the previous version completely. The site must exist. See below. |
| `create_upload_link` | `site` | Creates a one-time upload link for a `.zip` or `.tar.gz` of up to 512 MB. Returns `url`, `curl_command`, `expires_at`, `max_size_mb`. |
| `make_version_live` | `site`, `version` (optional) | Switches to a kept version; without `version`, back to the version before the live one. Safe to repeat. |
| `check_site_domains` | `site` | For each domain: `domain`, `state` (`points_here`, `points_elsewhere`, `no_record`, `lookup_failed`) and `found` (addresses it points to). Needs `-server-ip` to be configured; see [[DNS|DNS]]. |
| `list_redirects` | `site` (optional) | Lists the redirects you can see, or only the path redirects of one site. Each with `id`, `owner`, `source_domain`, `source_path`, `target`, `code`, `keep_path`, `keep_query`, `created_at`. Read-only. See [[Redirects|Redirects]]. |
| `set_redirect` | `id` (optional), `source_domain`, `source_path` (optional), `target`, `code` (optional, default 301), `keep_path`, `keep_query` (optional) | Creates a redirect, or replaces the one with `id`. Without `source_path` the whole domain redirects; with it, one path (`/old`) or a prefix (`/blog/*`) on a domain of one of your sites. Returns the redirect. |
| `delete_redirect` | `id`, `confirm_source` | Deletes a redirect. `confirm_source` must equal its `source_domain` followed by its `source_path` (like `www.example.com` or `example.com/old`), otherwise nothing happens. |
| `get_mail_status` | none | Whether the mail service runs and is up to date, the mail server's name (`mail_host`) and the ports for mail programs. Read-only. |
| `get_mail_records` | `domain` | The DNS records the mail domain needs (MX, SPF, DKIM, DMARC, SRV for mail programs) with what its nameservers answer and a status each (`ok`, `missing`, `different`, `conflict`, `unknown`), plus `can_apply`, `connection_id`, `zone` and `apply_note`. Read-only. |
| `set_up_mail_records` | `domain`, `confirm` (optional) | Writes those records into the domain's zone in your own DNS connection. Other TXT values are kept, an existing DMARC record is never changed, an SPF record that cannot be merged safely is skipped. Without `confirm`, nothing is written when replacing MX records that point elsewhere is needed (mail would move here): show the returned `changes` to the user and call again with `confirm: true` only if they agree. |
| `set_up_mail_host_spf` | none | Admins only: writes the SPF record of `mail_host` (`v=spf1 a -all`, or `a` added to an existing one) into its zone in your own DNS connection. |
| `get_mail_health` | none | Admins only: whether the mail service runs, `mail_host` points here and has an SPF record, the server addresses have it as PTR, outgoing port 25 works, the certificate is valid, every port listens, postmaster mail has somewhere to go and no blocklist refused this server's queries in the last 7 days. Read-only. |
| `list_mail_domains` | none | Lists the mail domains you can see: `domain`, `owner`, `active`, `problem` (why not active yet), `checked_at`, counts, `dkim_selector`, `dkim_record`, `created_at`. Read-only. |
| `enable_mail_domain` | `domain` | Enables e-mail for a domain of one of your sites or redirects, or one in a zone of your DNS connections (admins: any). It receives mail once its MX record points to `mail_host`. |
| `check_mail_domain` | `domain` | Looks at the MX record now and activates the domain if it points to `mail_host`. |
| `delete_mail_domain` | `domain`, `confirm_domain` | Deletes e-mail for the domain with every mailbox, alias and all stored mail. `confirm_domain` must equal `domain`. |
| `list_mailboxes` | `domain` (optional) | Lists mailboxes with `id`, `address`, quota, send limits, usage and whether sending is blocked. Read-only. |
| `set_mailbox_limits` | `id`, `quota_bytes`, `send_per_hour`, `send_per_day` (each optional) | Changes the limits; owners can only lower them. |
| `unblock_mailbox` | `id` | Lifts a sending block of the mail service (admins only). |
| `delete_mailbox` | `id`, `confirm_address` | Deletes a mailbox and its stored mail. `confirm_address` must equal its address. |
| `list_mail_aliases` | `domain` (optional) | Lists aliases with `id`, `address`, `catch_all`, `targets`. Read-only. |
| `set_mail_alias` | `id` (optional), `domain` (for new aliases), `local_part`, `targets` | Creates an alias, or replaces the one with `id`. `*` is the catch-all. An address is either a mailbox or an alias. |
| `delete_mail_alias` | `id`, `confirm_address` | Deletes an alias. `confirm_address` must equal its address. |
| `list_mail_imports` | `id` (mailbox) | Lists the imports of mail into a mailbox, newest first: `id`, `source` (user and server, or file name), `state`, `error`, counts of messages copied, skipped as already there and failed, `bytes`, `created`, `finished`. Read-only. |
| `cancel_mail_import` | `id` (mailbox), `import_id` | Stops an import that has not finished (copied mail stays), or removes a finished one from the list. |
| `set_mailbox_display_name` | `id`, `display_name` | Sets the name shown as the sender and in address books (at most 64 characters, no `<>"`); empty removes it. |
| `get_mailbox_out_of_office` | `id` | Whether the mailbox has an out-of-office reply, whether it is being sent now, its dates and who set it (not the person's own text). Read-only. |
| `set_mailbox_out_of_office` | `id`, `text`, `subject`, `start`, `end`, `zone`, `days`, `replace` (all but `id` and `text` optional) | Sets an out-of-office reply for a mailbox of your domains (for someone who left). The activity log records it and the person sees who set it. Fails while the person's filters were written in a mail program unless `replace` is set. |
| `clear_mailbox_out_of_office` | `id` | Turns the out-of-office reply off. |
| `list_mail_organisations` | none | Lists the organisations you manage (admins: all) with `id`, `name`, `owner` and `domains` (only the domains you may see). Read-only. |
| `set_mail_organisation` | `id` (optional), `name` | Creates an organisation, or renames the one with `id`. |
| `delete_mail_organisation` | `id`, `confirm_name` | Deletes an organisation; its domains stand alone again. `confirm_name` must equal its name. |
| `set_mail_domain_organisation` | `domain`, `organisation` | Puts a mail domain into one of your organisations, or lets it stand alone (`organisation` empty). Grouping domains of different owners is for admins only. |

Tools that change something need a `write` token. Read-only tools are `list_sites`, `get_site`, `check_site_domains`, `list_redirects`, `get_mail_status`, `get_mail_records`, `get_mail_health`, `list_mail_domains`, `list_mailboxes`, `list_mail_aliases`, `list_mail_imports` and `list_mail_organisations`. `list_mail_domains` also shows each domain's `organisation`, `list_mailboxes` the `display_name` and `app_passwords_only`. Tokens limited to sites cannot use the e-mail tools.

### `publish_files`

`files` is a list of `{path, content, encoding}`:

| Field | Meaning |
|---|---|
| `path` | relative to the site root, forward slashes, like `index.html` or `css/site.css`; no leading slash, no `..`, no empty or `.` segments |
| `content` | the file content |
| `encoding` | `utf8` (default) for text, `base64` for binary files such as images (standard alphabet, with padding) |

Limits: at least one file, at most 2000 files, 10 MB in total after decoding. A path cannot be given twice, and a path cannot be both a file and a folder. Include every file of the site, since files of earlier versions are not carried over. For bigger sites use `create_upload_link`.

### Upload links

A link looks like `https://<panel-domain>/api/uploads/<id>` (on the panel domain if `-panel-domain` is set, otherwise on the address the request came in on). The `curl_command` is:

```sh
curl --fail-with-body -X POST --data-binary @site.zip '<url>'
```

- Works **once** (the first attempt uses it up, successful or not), for **15 minutes**, only for the one site, and publishes as the person who created it. Treat the URL like a password.
- The link itself is the credential: no other authentication is needed for `POST /api/uploads/{id}`.
- Unknown, used and expired links all answer `404`.
- A person can have at most 20 unused links.
- Links live in memory, so a restart of ownpaneld invalidates them. A deleted token or changed role also affects its open links: they are re-checked when used.

## Not available

DNS connections and records cannot be managed through MCP; use the web interface or the [[API|API]].

Mailboxes cannot be created and their passwords cannot be set through MCP, on purpose: mailbox passwords should never pass through an AI agent. The same holds for app passwords: no tool creates, shows or revokes them, and "mail programs only with app passwords" is changed only in the web interface or the API. For the same reason imports of mail cannot be started (they take another account's password) and mailboxes cannot be downloaded (it reads all their mail); use the web interface or the [[API|API]]. Use the web interface or the [[API|API]] (`POST /api/mail/domains/{domain}/mailboxes`, `POST /api/mail/mailboxes/{id}/password`).
