# ownpanel

ownpanel is an open-source, self-hosted hosting panel. You run it on your own server; it serves your web sites and manages the pieces around them.

What it does today:

- **Static sites with automatic HTTPS.** Point a domain at the server, create a site for it, and the certificate is requested automatically.
- **Versions and going back.** Every publish creates a new version and makes it live. Earlier versions are kept, and you can switch back at any time.
- **Users and access tokens.** Admins and members sign in to a web interface. Scripts, CI and AI agents use access tokens that can be read-only and limited to some sites.
- **DNS at Hetzner.** Edit the DNS records of your Hetzner zones, check whether a site's domains point at the server, and fix them with one click.
- **MCP for AI agents.** The same operations are available as tools for AI assistants such as Claude Code, under the same permission rules.

## Status

ownpanel is **early and not production-hardened**. The core idea works for static sites, but expect rough edges. The installer has only been tested in containers, not yet on a real server (see [[Roadmap|Roadmap]]). Read [[Security|Security]] for the known gaps before you expose it to the Internet.

**E-mail and apps are planned, not built.** There is no mail server and no way to run applications yet.

## Pages

Start here:

- [[Getting Started|Getting-Started]]: run ownpanel on your machine, create the first admin and the first site
- [[Installation|Installation]]: install on a Linux server with systemd
- [[Configuration|Configuration]]: all command-line flags and the data directory

Using it:

- [[Sites and Publishing|Sites-and-Publishing]]: sites, domains, archives, versions
- [[Users and Access|Users-and-Access]]: roles, sign-in, access tokens, activity log
- [[DNS|DNS]]: connect Hetzner, manage records, point domains at the server
- [[MCP|MCP]]: connect AI agents
- [[API|API]]: the HTTP API
- [[Backup and Restore|Backup-and-Restore]]: back up and restore a server

Background:

- [[Security|Security]]: what is protected how, and what is not
- [[Architecture|Architecture]]: components and how they fit together
- [[Development|Development]]: build, test and contribute
- [[Roadmap|Roadmap]]: what is done, what is next

The source is at <https://github.com/danielgtmn/ownpanel>.
