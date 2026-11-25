# Security policy

ownpanel is installed as root and serves websites to the internet, so security reports are welcome and taken seriously.

## Reporting a vulnerability

**Please do not open a public issue.** Report it privately instead:

1. Go to [Security → Report a vulnerability](https://github.com/danielgtmn/ownpanel/security/advisories/new).
2. Describe what is affected, how to reproduce it, and what an attacker gains. Add the version (`ownpaneld version`, or Settings → Updates).

You get an answer within a week. Once a fix is released, the advisory is published, and you are credited unless you prefer not to be.

## Supported versions

ownpanel is before 1.0. Fixes go into `main` and ship in the next nightly build, and in the next stable release, which is cut promptly for security fixes. Only the **latest stable release** and the **current nightly** receive fixes. Update with Settings → Updates or `update.sh` ([how](https://github.com/danielgtmn/ownpanel/wiki/Installation#updating)).

## Scope

In scope: the daemon `ownpaneld`, its HTTP API and MCP server, the web interface, `install.sh`, `update.sh`, the update worker, and the publish action in `actions/`.

Out of scope: vulnerabilities in Caddy, Go or other dependencies themselves (report them upstream; tell us if ownpanel uses them unsafely), and the content of sites hosted with ownpanel.

How ownpanel protects sign-in, tokens, uploads, updates and stored secrets, and which gaps are known, is described in [Security](https://github.com/danielgtmn/ownpanel/wiki/Security) in the wiki. Known gaps listed there do not need a report.
