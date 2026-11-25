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

- `src/app/` the shell: the sidebar (`sidebar/`: rail of domain tiles with an "All" tile, an add menu, bottom entries and the account menu; a column with the rows of the selected tile; a drawer behind a menu button on phones; the family overview at `#/d/<domain>`), hash routing, first-run setup and sign-in screens, global styles. `src/app/setup/` is the first-run wizard an administrator walks through after creating the account (server addresses, panel domain, finish); it shows while the server reports `setup_complete: false`.
- `src/shared/` code every module may use: `api/` (requests, errors, the person's sites), `session/` (who is signed in, `useSession()`, name and password rules), `format/` (dates), `routing/`, `hooks/`, `ui/`, `domains/` (where a domain points, "point to this server" for any domain you control), `redirects/` (redirect rules, the add/edit dialog and list rows shared by the site page and the Redirects module), `families/` (domain families computed in the browser from sites, DNS domains and domain redirects; their status dot; the sidebar row type), `settings/` (panel settings shared by the wizard and the Settings page: addresses, panel domain with DNS records and live checks, certificate e-mail).
- `src/modules/<name>/` one area of the panel. It exports a `PanelModule` from `index.ts`.
- `src/modules/registry.ts` lists the modules; navigation and routes come only from this list. A module joins the sidebar through optional `PanelModule` fields: `railEntry` (icon tile at the bottom of the rail), `overviewRow` (row with a count under "All") and `familyRows` (rows in a domain's column). See `AGENTS.md`.

### Sidebar

Domains are grouped by their registrable domain: `blog.example.com` and `www.example.com` belong to `example.com`, `shop.co.uk` stays whole. The split is a heuristic for common two-part suffixes, not the full public suffix list (see `src/shared/families/suffix.ts`). A site with domains in several families shows up in each. A tile's dot is green when every domain of the family points to this server and every site in it is live, amber when one doesn't, hollow while lookups are still running or failed. The column's footer shows who is signed in; the server doesn't report whether the web server is in sync.

The panel's own address never shows up in the sidebar: it is managed only under Settings. Its registrable domain gets a tile only when sites, DNS domains or redirects bring it in.

A tile shows the icon of the domain's main site (the first live site on the domain itself or its `www.` address, else the first live site), loaded from `/api/sites/<name>/icon`, which only reads the site's published files. Without one, or when it fails to load, the tile shows initials. Right-click, long-press, Shift+F10 or the context-menu key on a tile opens a menu: icon or initials for this tile, initials for all domains, open the site, copy the domain. The rail can be expanded to show names next to the tiles (button at the bottom of the rail, or the `[` key); the phone drawer always shows names. These choices stay in the browser's local storage under `ownpanel.sidebar.v1` (`src/app/sidebar/railPrefs.ts`).

Addresses: every page keeps its address (`#/sites/blog`, `#/dns/1/example.com`, …) and opening one selects the matching tile and row. `#/d/example.com` shows a domain's overview, `#/redirects?family=example.com` the redirects of one domain, and `#/sites?new=1`, `#/redirects?new=1`, `#/dns?add=1` open the matching dialog (the add menu in the rail uses them).

Modules: `sites` (domains with their DNS status, main address with www handling, path redirects, publishing, versions), `redirects` (domain redirects with DNS status; everyone signed in), `dns` (connected DNS accounts, adding and deleting domains with their nameservers, records), `access` (personal access tokens), and the administrator-only `users` (People), `activity` and `settings` (server addresses, panel domain, certificate e-mail). A module can set `visible: (user) => ...` to hide itself from some people; hidden modules also disappear from the router.

Sign-in uses accounts with a login cookie; every request that changes something carries the `X-Ownpanel-Request` header (see `src/shared/api/client.ts`).

To add a module (DNS, e-mail, ...): create `src/modules/<name>/` with an `index.ts` that exports a `PanelModule`, then add one line to `registry.ts`. Modules never import from each other.
