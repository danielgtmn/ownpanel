# MCP

ownpaneld speaks the Model Context Protocol, so AI assistants can manage sites (create them, publish files, go back to an earlier version), redirects, e-mail and DNS records. The tools call the same operations as the API and are subject to the same permission checks.

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

Version ids come from `get_site`; a version id starts with a UTC timestamp. Other ids (`id`, `connection_id`, `mailbox_id`, `grantee_id`) and mail import ids (`import_id`) are strings such as `"42"`: pass them on exactly as a list returned them. Errors are returned as tool errors with a readable sentence.

### Sites

| Tool | Arguments | What it does |
|---|---|---|
| `list_sites` | none | Lists the sites you can manage, sorted by name. For each: name, domains, owner, id of the live version, all kept versions (newest first, each with `id`, `created_at`, `live`, `never_live` and `preview_url`, the address where the version can be opened, empty without a domain for previews). |
| `get_site` | `site` | Shows one site in the same form. |
| `create_site` | `name`, `domains` (optional list) | Creates a new, empty site you own. Fails if the name or a domain is taken. Nothing is served until something is published. Needs a `write` token that is not limited to sites. |
| `set_site_domains` | `name`, `domains` | Replaces the whole domain list of an existing site. Domains not listed stop being served. Safe to repeat. |
| `delete_site` | `name`, `confirm_name` | Permanently deletes the site with all its files and versions. `confirm_name` must equal `name`, otherwise nothing happens. Needs a `write` token that is not limited to sites. |
| `publish_files` | `site`, `files`, `live` (optional, default `true`) | Publishes files given directly as a new version, replacing the previous version completely. With `live: false` the live version stays and the new one can be checked at its preview address first. Returns `site`, `version`, `live` and `preview_url`. The site must exist. See below. |
| `create_upload_link` | `site`, `live` (optional, default `true`) | Creates a one-time upload link for a `.zip` or `.tar.gz` of up to 512 MB; with `live: false` the uploaded version is not made live. Returns `url`, `curl_command`, `expires_at`, `max_size_mb`. |
| `build_site` | `site`, `files`, `command`, `output`, `image`, `env` and `secrets` (optional), `live` (optional, default `true`) | Administrators only. Builds the site from its source files on the server (files as for `publish_files`, same limits) and publishes the output folder as a new version; answers when the build has ended. Returns `status` (`succeeded`, `failed`, `cancelled`, `skipped`), `reason`, `message`, `exit_code`, `log_tail` (secrets blanked out), and after success `version`, `live` and `preview_url`. A failed build is an answer, not a tool error, and changes nothing. See [[Sites and Publishing\|Sites-and-Publishing#building-a-site-from-its-source]]. |
| `make_version_live` | `site`, `version` (optional) | Switches to a kept version, also one published with `live: false`; without `version`, back to the last version before the live one that was live itself. Safe to repeat. |
| `check_site_domains` | `site` | For each domain: `domain`, `state` (`points_here`, `points_elsewhere`, `no_record`, `lookup_failed`) and `found` (addresses it points to). Needs `-server-ip` to be configured; see [[DNS|DNS]]. |

### Site sources and runs

| Tool | Arguments | What it does |
|---|---|---|
| `get_site_source` | `site` | The repository and branch the site is linked to (`source`, or `null`), with `publish_on_push`, `status` (`linked`, `connection_missing`, `suspended`, `repository_missing`, `unchecked`) and `problem`, `publishing_available` (`false`: building and publishing on push is not available yet, so a push only records a run that waits), and `last_run` (the newest run, or `null`). Read-only; asks GitHub. |
| `list_site_source_repositories` | `site` | The repositories you may link the site to, each with `installation_id`, `repository_id`, `full_name`, `account`, `private`, `archived`, `default_branch`. Empty for anyone but administrators. Read-only. |
| `list_site_source_branches` | `site`, `installation_id`, `repository_id` | The branches of such a repository and its `default_branch`. Read-only. |
| `link_site_source` | `site`, `installation_id`, `repository_id`, `branch`, `publish_on_push` | Links the site to the repository and branch, replacing its link (also to change the branch or `publish_on_push`). GitHub is asked whether both exist. Needs write access to the site and an administrator. Destructive (replaces the previous link), idempotent. |
| `unlink_site_source` | `site` | Removes the link; the site and its files stay. Destructive. |
| `list_site_runs` | `site` | The site's newest runs, newest first: one per push to the linked branch while `publish_on_push` is on and one per `start_site_build`, each with `id`, `trigger` (`push`, `manual`), `status` (`queued`, `running`, `succeeded`, `failed`, `cancelled`, `skipped`), `repository`, `branch`, `commit`, `message` (first line, untrusted text), `author_name`, `author_login`, `pusher`, `detail`, `waiting` (set while the server cannot build), the times and `release` (the version a succeeded run published); `kept` is how many the panel keeps (30). Read-only. |
| `start_site_build` | `site` | Build now: records a run of the newest commit of the linked branch and returns it queued; the panel builds it with the site's build settings and makes the result live. A failed build changes nothing. Asks GitHub. |
| `cancel_site_run` | `site`, `run_id` | Cancels a queued run or stops the build of a running one; the site stays as it is. Refused for runs that have ended. Destructive. |
| `read_site_run_log` | `site`, `run_id`, `offset` (optional) | The output of a run's build from `offset` on (up to 256 KB per call), with `next`, `live` (still building: call again with `next`), `done` and `truncated`. Secret values are hidden; the text is untrusted. Read-only. |
| `get_site_build_settings` | `site` | How the site is built: `command`, `output`, `image`, `variables` (names; secret ones without value), `configured`, the offered `images`. Read-only. |
| `set_site_build_settings` | `site`, `command`, `output`, `image` (optional), `variables` (optional: `name`, `value`, `secret`) | Replaces the build settings; `variables` is the complete list, a variable without `value` keeps its stored one. Destructive (replaces), idempotent. See [[Sites and Publishing\|Sites-and-Publishing#build-settings]]. |
| `propose_site_build_settings` | `site` | Looks at the newest commit of the linked branch and suggests `preset`, `command`, `output`, `image` with `confidence`, `buildable` and `notes`. Saves nothing; needs a `write` token. Read-only, asks GitHub. |

### GitHub

| Tool | Arguments | What it does |
|---|---|---|
| `get_github_status` | none | Administrators only: whether GitHub is connected (`connected`), whether the panel can be connected (`can_connect`, `problem`), and each connection with `id` (the panel's, a string), `name`, `owner`, `owner_type`, `problem` and its `installations` (`installation_id`, `account`, `account_type`, `repository_selection`, `repository_count`, `suspended`). Read-only. Connecting and removing happen in the web interface ([[GitHub|GitHub]]): GitHub asks for a confirmation in the browser. |
| `list_github_repositories` | `installation_id` | Administrators only: the repositories an installation may see (`installation_id` is GitHub's number), each with `id` (GitHub's number), `full_name`, `private`, `archived`, `default_branch`, `url`, and `truncated`. Read-only. |

### Redirects

| Tool | Arguments | What it does |
|---|---|---|
| `list_redirects` | `site` (optional) | Lists the redirects you can see, or only the path redirects of one site. Each with `id`, `owner`, `source_domain`, `source_path`, `target`, `code`, `keep_path`, `keep_query`, `created_at`. Read-only. See [[Redirects|Redirects]]. |
| `set_redirect` | `id` (optional), `source_domain`, `source_path` (optional), `target`, `code` (optional, default 301), `keep_path`, `keep_query` (optional) | Creates a redirect, or replaces the one with `id`. Without `source_path` the whole domain redirects; with it, one path (`/old`) or a prefix (`/blog/*`) on a domain of one of your sites. Returns the redirect. |
| `delete_redirect` | `id`, `confirm_source` | Deletes a redirect. `confirm_source` must equal its `source_domain` followed by its `source_path` (like `www.example.com` or `example.com/old`), otherwise nothing happens. |

### Mail service and mail domains

| Tool | Arguments | What it does |
|---|---|---|
| `get_mail_status` | none | Whether the mail service runs and is up to date, the mail server's name (`mail_host`) and the ports for mail programs. Read-only. |
| `get_mail_records` | `domain` | The DNS records the mail domain needs (MX, SPF, DKIM, DMARC, SRV for mail programs) with what its nameservers answer and a status each (`ok`, `missing`, `different`, `conflict`, `unknown`), plus `can_apply`, `connection_id`, `zone` and `apply_note`. Read-only. |
| `set_up_mail_records` | `domain`, `confirm` (optional) | Writes those records into the domain's zone in your own DNS connection. Other TXT values are kept, an existing DMARC record is never changed, an SPF record that cannot be merged safely is skipped. Without `confirm`, nothing is written when replacing MX records that point elsewhere is needed (mail would move here): show the returned `changes` to the user and call again with `confirm: true` only if they agree. |
| `set_up_mail_host_spf` | none | Admins only: writes the SPF record of `mail_host` (`v=spf1 a -all`, or `a` added to an existing one) into its zone in your own DNS connection. |
| `get_mail_health` | none | Admins only: whether the mail service runs, `mail_host` points here and has an SPF record, the server addresses have it as PTR, outgoing port 25 works, the certificate is valid, every port listens, postmaster mail has somewhere to go and no blocklist refused this server's queries in the last 7 days. Read-only. |
| `list_mail_domains` | none | Lists the mail domains you can see: `domain`, `owner`, `active`, `problem` (why not active yet), `checked_at`, counts, `dkim_selector`, `dkim_record`, `created_at`. Read-only. |
| `enable_mail_domain` | `domain` | Enables e-mail for a domain of one of your sites or redirects, or one in a zone of your DNS connections (admins: any). It receives mail once its MX record points to `mail_host`. |
| `check_mail_domain` | `domain` | Looks at the MX record now and activates the domain if it points to `mail_host`. |
| `delete_mail_domain` | `domain`, `confirm_domain` | Deletes e-mail for the domain with every mailbox, alias and all stored mail. `confirm_domain` must equal `domain`. |

### Mailboxes and aliases

| Tool | Arguments | What it does |
|---|---|---|
| `list_mailboxes` | `domain` (optional) | Lists mailboxes with `id`, `address`, quota, send limits, usage and whether sending is blocked. Read-only. |
| `set_mailbox_limits` | `id`, `quota_bytes`, `send_per_hour`, `send_per_day` (each optional) | Changes the limits; owners can only lower them. |
| `unblock_mailbox` | `id` | Lifts a sending block of the mail service (admins only). |
| `delete_mailbox` | `id`, `confirm_address` | Deletes a mailbox and its stored mail. `confirm_address` must equal its address. |
| `list_mail_aliases` | `domain` (optional) | Lists aliases with `id`, `address`, `catch_all`, `targets`, `sender_policy`, `hide_from_directory`. Read-only. |
| `set_mail_alias` | `id` (optional), `domain` (for new aliases), `local_part`, `targets`, `sender_policy`, `hide_from_directory` (both optional) | Creates an alias, or replaces the one with `id`. `*` is the catch-all. An address is either a mailbox or an alias. `sender_policy` makes it a distribution group: who may write to it, `anyone`, `organisation` or `members` (not for the catch-all); `hide_from_directory` leaves it out of the organisation's address book. Left out, both stay (`anyone` and `false` for a new alias). |
| `delete_mail_alias` | `id`, `confirm_address` | Deletes an alias. `confirm_address` must equal its address. |
| `list_mail_imports` | `id` (mailbox) | Lists the imports of mail into a mailbox, newest first: `id`, `source` (user and server, or file name), `state`, `error`, counts of messages copied, skipped as already there and failed, `bytes`, `created`, `finished`. Read-only. |
| `cancel_mail_import` | `id` (mailbox), `import_id` | Stops an import that has not finished (copied mail stays), or removes a finished one from the list. |

### Mailbox settings

| Tool | Arguments | What it does |
|---|---|---|
| `set_mailbox_display_name` | `id`, `display_name` | Sets the name shown as the sender and in address books (at most 64 characters, no `<>"`); empty removes it. |
| `get_mailbox_out_of_office` | `id` | Whether the mailbox has an out-of-office reply, whether it is being sent now, its dates and who set it (not the person's own text). Read-only. |
| `set_mailbox_out_of_office` | `id`, `text`, `subject`, `start`, `end`, `zone`, `days`, `replace` (all but `id` and `text` optional) | Sets an out-of-office reply for a mailbox of your domains (for someone who left). The activity log records it and the person sees who set it. Fails while the person's filters were written in a mail program unless `replace` is set. |
| `clear_mailbox_out_of_office` | `id` | Turns the out-of-office reply off. |

### Organisations

| Tool | Arguments | What it does |
|---|---|---|
| `list_mail_organisations` | none | Lists the organisations you manage (admins: all) with `id`, `name`, `owner` and `domains` (only the domains you may see). Read-only. |
| `set_mail_organisation` | `id` (optional), `name` | Creates an organisation, or renames the one with `id`. |
| `delete_mail_organisation` | `id`, `confirm_name` | Deletes an organisation; its domains stand alone again. `confirm_name` must equal its name. |
| `set_mail_domain_organisation` | `domain`, `organisation` | Puts a mail domain into one of your organisations, or lets it stand alone (`organisation` empty). Grouping domains of different owners is for admins only. |

### Shared mailboxes

| Tool | Arguments | What it does |
|---|---|---|
| `set_mailbox_kind` | `id`, `kind` (optional, only `shared`), `keep_sent_copy` (optional) | Makes a personal mailbox shared: its password and app passwords are removed, everyone signed in to it is signed out, and its access to other mailboxes ends. `keep_sent_copy` (shared mailboxes) keeps mail sent for it in its Sent folder. Making a mailbox personal again needs a password and is done in the web interface. Returns the mailbox. |
| `set_mailbox_access` | `id` (a shared mailbox), `address`, `rights` | Gives a personal mailbox of the same organisation access to a shared mailbox, or changes its rights: `read`, `write` (includes read), `manage`, `send-as`, `send-on-behalf`. Its folders then appear under `Shared/<address>` in that person's mail program. Returns the shared mailbox. |
| `remove_mailbox_access` | `id`, `grantee_id` | Takes away a mailbox's access (`mailbox_id` from `shared_with`) to a shared mailbox, or to a personal mailbox its owner shared. Mail programs with one of its folders open are disconnected. |

### DNS domains

| Tool | Arguments | What it does |
|---|---|---|
| `list_dns_domains` | `available` (optional) | Lists the domains (zones) in the panel through your own DNS connections: chosen ones and those your own sites, redirects or e-mail use (for administrators also the panel domain). Each with `connection_id`, `connection_name`, `domain`, `read_only`, `nameservers`, `adopted`, `used_by`; connections that fail are in `problems`. With `available: true`, the zones not in the panel yet. Read-only. |
| `manage_dns_domain` | `connection_id`, `domain` | Manages a zone of your own connection in the panel. Nothing changes at the provider. Safe to repeat. |
| `stop_managing_dns_domain` | `connection_id`, `domain` | Takes a chosen zone out of the panel; it stays at the provider. Refused while something of yours uses it. |
| `list_dns_record_templates` | none | Lists the record templates (`id`, `title`, `description`, `params`, `own_mail`): ready-made records for Google Workspace, Microsoft 365 or Fastmail mail, or a text that proves you own the domain. `own_mail` marks e-mail on this server, which is set up with `set_up_mail_records` instead. Read-only. |
| `apply_dns_record_template` | `connection_id`, `domain`, `template`, `params` (optional), `confirm` (optional) | Without `confirm` (the default) only returns what would change: for each record set `action` (`add`, `change`, `ok`, `conflict`), `current` and `new` values and a `note`; nothing is written, also with a `read` token. Show it to the user and call again with `confirm: true` only if they agree. Other TXT values are kept, the template's sender is merged into an existing SPF record, replaced MX records move the domain's mail. If any change is a `conflict`, nothing is written. Safe to repeat. |
| `list_domains` | | Lists the domains you added whose DNS is managed elsewhere, each with `domain`, `created_at` and `used_by`. Read-only. |
| `add_domain` | `domain` | Adds a domain whose DNS is managed elsewhere (a registrable domain like `example.com`), so it shows up in the panel on its own. Nothing changes at any provider. Safe to repeat. |
| `remove_domain` | `domain` | Removes such a domain from your list; nothing changes at any provider. `still_used_by` says what of yours still keeps it in the panel. |

### DNS records

| Tool | Arguments | What it does |
|---|---|---|
| `list_dns_connections` | none | Lists your connections to DNS provider accounts: `id`, `provider`, `name`, `owner`, `created_at`, `last_checked_at`, `last_error`, `can_write`, `domain_count`. Never a token. Administrators also see other people's connections but can only use their own. Read-only. |
| `list_dns_records` | `connection_id`, `domain` | Lists the record sets of a zone of your own connection as the provider has them now, `@` first: `name`, `type`, `ttl` (`null` is the zone's default), `values`, `locked`. Read-only. |
| `set_dns_record` | `connection_id`, `domain`, `name`, `type`, `ttl` (optional), `values` | Creates or replaces the record set of one name and type; `values` replace all its values. `name` is relative to the zone (`@`, `www`). TXT values are plain text without quotes. Returns the record set as written. Safe to repeat. |
| `delete_dns_record` | `connection_id`, `domain`, `name`, `type` | Deletes the record set of one name and type. A record set that does not exist is not an error; SOA and the zone's own NS records cannot be deleted. |
| `check_domain` | `domain` | For one domain of your sites or whole-domain redirects: `state` and `found` as in `check_site_domains`, and `zone` (`connection_id`, `connection_name`, `zone`, `record_name`): where `point_domain_at_server` would write, `null` if none of your connections holds it. Read-only. |
| `point_domain_at_server` | `domain` | "Point to this server" for one domain of your sites or whole-domain redirects: writes A and AAAA records with the server's addresses (TTL 300) into the zone of your own connection that holds it, and removes an address record set of a family the server has no address in. Refused if the name has a CNAME. Returns `changed`, empty if it already pointed here. |

Tools that change something need a `write` token. Read-only tools are `list_sites`, `get_site`, `check_site_domains`, `list_redirects`, `get_mail_status`, `get_mail_records`, `get_mail_health`, `list_mail_domains`, `list_mailboxes`, `list_mail_aliases`, `list_mail_imports`, `list_mail_organisations`, `list_dns_domains`, `list_domains`, `get_github_status`, `list_github_repositories`, `get_site_source`, `list_site_source_repositories`, `list_site_source_branches`, `list_site_runs`, `read_site_run_log`, `get_site_build_settings` and `propose_site_build_settings` (which still needs a `write` token, like the dialog it stands for). The site source tools, `start_site_build` and `propose_site_build_settings` ask GitHub and are marked as reaching outside the panel. `list_mail_domains` also shows each domain's `organisation`, `list_mailboxes` the `display_name`, `app_passwords_only`, `kind` (`personal` or `shared`), `keep_sent_copy`, `shared_with` (who has access, each with `mailbox_id`, `address`, `display_name`, `rights`, `active`) and `access_to` (the mailboxes it may use, with `kind`); `active` is false when an access is not in effect because the domains are no longer in one organisation. Tokens limited to sites cannot use the e-mail, DNS and domain tools.

The DNS tools follow the rules of the web interface ([[DNS|DNS]]): records are read and changed only through **your own** DNS connections, a connection of another person answers "not found" (an administrator is told whose it is), and pointing a domain needs a domain of a site or whole-domain redirect you can see. `list_dns_connections`, `list_dns_records` and `check_domain` are read-only as well; a token limited to sites may still use `check_domain` and `point_domain_at_server` for the domains of its sites.

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

Connecting, checking and removing DNS provider accounts is not possible through MCP, on purpose: connecting one needs the provider's API token, which should never pass through an AI agent. Use the web interface or the [[API|API]]; once connected, the account's domains and records can be used through the tools above. Choosing which domains are managed in the panel is possible (`manage_dns_domain`), and so is adding a domain whose DNS is managed elsewhere (`add_domain`).

Applying a record template is possible (`apply_dns_record_template`): it writes only after a preview the user agreed to.

Connecting a GitHub account or removing one is not possible through MCP: GitHub asks the person to confirm in the browser, so it happens in the web interface (Settings → GitHub, see [[GitHub|GitHub]]). `get_github_status` and `list_github_repositories` only look.

Mailboxes cannot be created or made personal again, and their passwords cannot be set, through MCP, on purpose: mailbox passwords should never pass through an AI agent. The same holds for app passwords: no tool creates, shows or revokes them, and "mail programs only with app passwords" is changed only in the web interface or the API. For the same reason imports of mail cannot be started (they take another account's password) and mailboxes cannot be downloaded (it reads all their mail); use the web interface or the [[API|API]]. Use the web interface or the [[API|API]] (`POST /api/mail/domains/{domain}/mailboxes`, `POST /api/mail/mailboxes/{id}/password`).
