# Roadmap

ownpanel is early. This page separates what exists from what is only planned. Planned items are intentions, not promises, and have no dates.

## Done

- **Static sites** with automatic HTTPS, served by Caddy; versions, making an older version live, deleting ([[Sites and Publishing|Sites-and-Publishing]]).
- **Users and access tokens:** admins and members, sign-in sessions, read-only and site-limited tokens, an activity log ([[Users and Access|Users-and-Access]]).
- **Web interface** for all of the above.
- **MCP** for AI agents, with the same permission rules ([[MCP|MCP]]).
- **DNS with Hetzner:** records of Hetzner zones, domain status, "point to this server" ([[DNS|DNS]]).
- **Backups** with a command, and a documented restore ([[Backup and Restore|Backup-and-Restore]]).
- **Install files** for a Linux server with systemd ([[Installation|Installation]]).

## Next

1. **E-mail**, as a self-hosted service that also sends mail itself. Not built: there is no mail server, mailbox or sending in the code today.
2. **Apps in containers.** Not built: today only static sites can be hosted.

## Open items

- **The install script has not been tested on a real server.** Try it on a throwaway machine first.
- **No per-site system users or quotas:** all site files belong to one user.
- **Sign-in throttling is in memory** and resets on restart; it trusts `X-Forwarded-For` from local connections.
- **No Content Security Policy** for the web interface.
- **Only Hetzner** is supported as a DNS provider.
- **Linux amd64 only** in the `make build-linux` target; other architectures need a manual build.
- **No uninstall script;** see [[Installation|Installation]] for the manual steps.
- Details on known gaps: [[Security|Security]].
