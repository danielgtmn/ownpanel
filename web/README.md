# ownpanel web

The panel's web interface: React 19, TypeScript, Vite and ui-sdk.

## Run

```sh
npm install
npm run dev      # http://localhost:5173, forwards /api to http://127.0.0.1:8600
```

Start the ownpanel daemon on port 8600 first.

## Build and check

```sh
npm run build    # type check, then bundle into ../internal/webui/dist
npm run lint
npm test -- --run
```

## Structure

- `src/app/` the shell: header with account menu, navigation, hash routing, first-run setup and sign-in screens, global styles. `src/app/setup/` is the first-run wizard an administrator walks through after creating the account (server addresses, panel domain, finish); it shows while the server reports `setup_complete: false`.
- `src/shared/` code every module may use: `api/` (requests, errors, the person's sites), `session/` (who is signed in, `useSession()`, name and password rules), `format/` (dates), `routing/`, `hooks/`, `ui/`, `domains/` (where a domain points, "point to this server" for any domain you control), `redirects/` (redirect rules, the add/edit dialog and list rows shared by the site page and the Redirects module), `settings/` (panel settings shared by the wizard and the Settings page: addresses, panel domain with DNS records and live checks, certificate e-mail).
- `src/modules/<name>/` one area of the panel. It exports a `PanelModule` from `index.ts`.
- `src/modules/registry.ts` lists the modules; navigation and routes come only from this list.

Modules: `sites` (domains with their DNS status, main address with www handling, path redirects, publishing, versions), `redirects` (domain redirects with DNS status; everyone signed in), `dns` (connected DNS accounts, adding and deleting domains with their nameservers, records), `access` (personal access tokens), and the administrator-only `users` (People), `activity` and `settings` (server addresses, panel domain, certificate e-mail). A module can set `visible: (user) => ...` to hide itself from some people; hidden modules also disappear from the router.

Sign-in uses accounts with a login cookie; every request that changes something carries the `X-Ownpanel-Request` header (see `src/shared/api/client.ts`).

To add a module (DNS, e-mail, ...): create `src/modules/<name>/` with an `index.ts` that exports a `PanelModule`, then add one line to `registry.ts`. Modules never import from each other.
