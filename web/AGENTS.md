# web

The panel's web interface: React 19 + TypeScript + Vite, built with ui-sdk.

## ui-sdk

Before using ui-sdk, follow the `ui-sdk` skill if it's installed
(`.agents/skills/ui-sdk/SKILL.md`), and read
`node_modules/@ui-sdk/react/docs/llms.txt`. To build a new component, also
search `node_modules/@ui-sdk/react/docs/core.md` by heading, and read
`rendering.md` next to it.

## Structure

- `src/app/` is the shell: layout, sidebar navigation, routing, global styles.
- `src/account/` is the account area for mailbox users. It renders instead of
  the panel when the page was opened on the mail server's name (the panel
  marks `index.html` with `<meta name="ownpanel-mail-host">` there), never
  loads the panel's session and talks only to `/account/api/` of the mail
  service. It is loaded on demand like a page.
- `src/shared/` holds code every module may use: `api/` (API client, shared
  lookups), `session/` (signed-in person, `useSession()`, name and password
  rules), `format/` (dates), `routing/`, `hooks/`, `ui/` (small UI pieces and
  the inline icons; there is no icon library), `families/` (domain families,
  their status and the row type of the sidebar).
- `src/modules/<name>/` is one self-contained area of the panel (for example
  `sites`). A module exports a single `PanelModule` from its `index.ts` and is
  listed in `src/modules/registry.ts`. Modules never import from each other.
  `PanelModule.visible` hides a module (and its address) from people it
  doesn't apply to, for example administrator-only areas.
- Pages load on demand. `PanelModule.page` is a loader,
  `() => import('./SitesPage.tsx').then((page) => page.SitesPage)`, and the
  shell renders it through `ModulePage` (`src/app/ModulePage.tsx`), which
  shows the loading state and, if the download fails (for example after an
  update), an error with a reload action. Everything else in a module's
  `index.ts` (label, `railEntry`, `overviewRow`, `familyRows`, `visible`) is
  in the first download: keep it light and never import a page there
  statically, or the page lands back in the main bundle. Other parts that
  only some people see use `lazyPage()` inside `PageFrame` the same way
  (the domain overview, the first-run wizard).
- `vite.config.ts` puts React and the parts of ui-sdk the first screen needs
  into their own files so they stay cached across updates.

## Navigation

The shell (`src/app/Shell.tsx`, `src/app/sidebar/`) draws a sidebar organised
by domain: a narrow rail with an "All" tile and an add menu at the top, one
tile per domain family in the middle (only this part scrolls) and the module
entries and account at the bottom, and a column with the rows of the
selected tile. Modules hang
into it only through optional fields on `PanelModule`:

- `railEntry: { icon, label? }` adds an icon tile to the rail's bottom group
  (Settings, People, …). It follows `visible`, so admin-only modules stay
  hidden from members.
- `overviewRow: { icon, label?, count }` adds a row with a count to the
  column of the "All" tile. `count` gets everyone's `DomainSources`.
- `familyRows(family)` returns the rows a module contributes to one domain
  family (`FamilyRow` from `src/shared/families`): label, link, icon, an
  optional hint, count or status dot, and `isCurrent(route)`, which decides
  which row and which tile light up when a page is opened by its address.

Row order follows `registry.ts`. A family is a registrable domain
(`registrableDomain()` in `src/shared/families/suffix.ts`, a heuristic, see the
comment there); `#/d/<family>` opens its overview. The sidebar reloads after
every request that changed something (`onDataChange` in `src/shared/api`).
Pages can be opened with a query that starts an action: `#/sites?new=1`,
`#/redirects?new=1`, `#/dns?add=1`; `#/redirects?family=example.com` filters
the list. Call `clearQuery()` once such a query is used up.

## Rules

- Text shown to people describes what happens for them. No library,
  protocol or implementation names in labels, titles, hints or messages.
- Passwords and token secrets are never written to storage or logs.
- The build output goes to `../internal/webui/dist`, which the Go daemon embeds.
