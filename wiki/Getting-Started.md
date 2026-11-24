# Getting Started

This page runs ownpanel on your own machine, with self-signed certificates and unprivileged ports. To install it on a server, see [[Installation|Installation]].

## Prerequisites

- Go (the version in `go.mod`, currently 1.26)
- Node.js, for the web interface (see [[Development|Development]])
- A checkout of the repository

## Start the daemon and Caddy

```sh
scripts/dev.sh
```

The script:

- installs Caddy into `bin/` with `go install` if `bin/caddy` is missing,
- builds `bin/ownpaneld` (without the web interface),
- keeps everything in `./data` (Caddy's admin API is a Unix socket there),
- starts Caddy, then `ownpaneld -data ./data -tls internal -http-port 8080 -https-port 8443 -caddy-admin unix://…`.

Extra arguments are passed on to `ownpaneld`. Stop with Ctrl-C.

The panel itself listens on `http://127.0.0.1:8600`.

## Start the web interface

The dev daemon is built without the web app, so run the Vite dev server in a second terminal:

```sh
cd web
npm install
npm run dev      # http://localhost:5173, forwards /api to http://127.0.0.1:8600
```

Open <http://localhost:5173>.

Alternatively, `make build` builds the web app and embeds it into `bin/ownpaneld`; the daemon then serves it on its own address.

## First run: the setup code

While no user exists, ownpaneld writes a one-time **setup code** to `./data/setup-code` (readable only by the daemon's user) and logs where it is. The code looks like `ABCD-EFGH-…` (letters and digits, grouped by four).

1. Open the panel. It asks for the setup code.
2. Choose the first admin's name (3 to 32 characters: lowercase letters, digits, dots, underscores, hyphens) and a password (10 to 200 characters).
3. The setup code file is deleted and setup is closed for good.

```sh
cat data/setup-code
```

## Your first site

In the web interface, open **Sites** and create a site: a name (lowercase letters, digits and hyphens, up to 63 characters) and one or more domains.

Or with the API, using an access token (create one under your account in the web interface; see [[Users and Access|Users-and-Access]]):

```sh
export TOKEN=op_…
curl -X PUT -H "Authorization: Bearer $TOKEN" \
  -d '{"domains":["demo.example.test"]}' \
  http://127.0.0.1:8600/api/sites/demo
```

Until something is published, the site answers `503` with "This site has no content yet."

## Publish an archive

Make a `.zip` or `.tar.gz` whose root is the web root (a single top-level folder is unwrapped):

```sh
mkdir -p site && echo '<h1>Hello</h1>' > site/index.html
tar -czf site.tar.gz -C site .
curl -X POST -H "Authorization: Bearer $TOKEN" \
  --data-binary @site.tar.gz \
  http://127.0.0.1:8600/api/sites/demo/deployments
```

In the web interface, the same is a file upload on the site's page.

Because the demo domain does not exist in DNS, pin it to your machine when fetching:

```sh
curl -k --resolve demo.example.test:8443:127.0.0.1 https://demo.example.test:8443/
```

(`-k` accepts the self-signed certificate.)

Publish again to create a second version; to go back, use the version list in the web interface or `POST /api/sites/demo/rollback`. Details are in [[Sites and Publishing|Sites-and-Publishing]].

## Run the end-to-end check

In a second terminal, while `scripts/dev.sh` runs:

```sh
INSECURE=1 scripts/smoke.sh
```

On a fresh `./data` the script performs the first-run setup itself and prints the admin password it chose, so run it before you create your own admin. Later runs need `ADMIN_NAME` and `ADMIN_PASSWORD`, or an access token in `OWNPANEL_TOKEN`. It creates a site, deploys, redeploys, rolls back, rejects a malicious archive and deletes the site again.
