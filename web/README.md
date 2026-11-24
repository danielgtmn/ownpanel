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

- `src/app/` the shell: header with account menu, navigation, hash routing, first-run setup and sign-in screens, global styles.
- `src/shared/` code every module may use: `api/` (requests, errors, the person's sites), `session/` (who is signed in, `useSession()`, name and password rules), `format/` (dates), `routing/`, `hooks/`, `ui/`.
- `src/modules/<name>/` one area of the panel. It exports a `PanelModule` from `index.ts`.
- `src/modules/registry.ts` lists the modules; navigation and routes come only from this list.

Modules: `sites`, `dns` (connected DNS accounts and records), `access` (personal access tokens), and the administrator-only `users` (People) and `activity`. A module can set `visible: (user) => ...` to hide itself from some people; hidden modules also disappear from the router.

Sign-in uses accounts with a login cookie; every request that changes something carries the `X-Ownpanel-Request` header (see `src/shared/api/client.ts`).

To add a module (DNS, e-mail, ...): create `src/modules/<name>/` with an `index.ts` that exports a `PanelModule`, then add one line to `registry.ts`. Modules never import from each other.
