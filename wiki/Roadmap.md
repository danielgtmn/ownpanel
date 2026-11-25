# Roadmap

ownpanel is early. This page separates what exists from what is only planned. Planned items are intentions, not promises, and have no dates.

## Done

- **Static sites** with automatic HTTPS, served by Caddy; versions, making an older version live, deleting ([[Sites and Publishing|Sites-and-Publishing]]).
- **Users and access tokens:** admins and members, sign-in sessions, read-only and site-limited tokens, an activity log ([[Users and Access|Users-and-Access]]).
- **Web interface** for all of the above.
- **MCP** for AI agents, with the same permission rules ([[MCP|MCP]]).
- **DNS with Hetzner:** add and delete domains, edit records, domain status, "point to this server" ([[DNS|DNS]]).
- **Redirects:** main address with or without www, path redirects, whole-domain redirects ([[Redirects|Redirects]]).
- **First-run setup wizard** in the browser and a Settings page for the panel's domain and the server's addresses ([[Getting Started|Getting-Started]]).
- **Backups** with a command, and a documented restore ([[Backup and Restore|Backup-and-Restore]]).
- **Installer** for Debian and Ubuntu with upgrade and uninstall, stable releases from the `release` branch and a nightly build of every change to `main` ([[Installation|Installation]]).
- **Navigation by domain:** a sidebar with one entry per domain that bundles its site, DNS and redirects.
- **Updates** from the panel (Settings → Updates, optionally automatic) or with `update.sh`, with a backup first and an automatic rollback ([[Installation|Installation#updating]]).

## Next

The live plan is the **[roadmap board](https://github.com/users/danielgtmn/projects/5)**. It sorts every issue into *Now*, *Next* and *Later* and by area. Planned releases are [milestones](https://github.com/danielgtmn/ownpanel/milestones):

- **v0.2.0:**
  - publish from GitHub with an Action ([#56](https://github.com/danielgtmn/ownpanel/issues/56));
  - a test on a real server ([#6](https://github.com/danielgtmn/ownpanel/issues/6));
  - hardening.
- **v0.3.0:**
  - build sites from a GitHub repository on the server ([#55](https://github.com/danielgtmn/ownpanel/issues/55));
  - preview versions;
  - DNS for agents.
- **v0.4.0:** e-mail, first version, as a self-hosted service that also sends mail itself ([#21](https://github.com/danielgtmn/ownpanel/issues/21)). Not built: there is no mail server, mailbox or sending in the code today.
- **Later:** apps in containers ([#32](https://github.com/danielgtmn/ownpanel/issues/32)). Not built: today only static sites can be hosted.

All planned work is tracked as [issues](https://github.com/danielgtmn/ownpanel/issues), labelled by area (`area: web`, `area: email`, `area: dns`, …).

## Open items

- **The installer and the updater have only been tested in containers,** not on a real server ([#6](https://github.com/danielgtmn/ownpanel/issues/6)). Try it on a throwaway machine first.
- **The panel stays reachable by IP address** after setup when no panel domain is set ([#33](https://github.com/danielgtmn/ownpanel/issues/33)).
- **No per-site system users or quotas:** all site files belong to one user.
- **Sign-in throttling** trusts `X-Forwarded-For` from local connections.
- **No Content Security Policy** for the web interface.
- **Only Hetzner** is supported as a DNS provider ([#18](https://github.com/danielgtmn/ownpanel/issues/18)).
- Details on known gaps: [[Security|Security]].
