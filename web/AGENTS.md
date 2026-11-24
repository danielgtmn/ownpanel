# web

The panel's web interface: React 19 + TypeScript + Vite, built with ui-sdk.

## ui-sdk

Before using ui-sdk, follow the `ui-sdk` skill if it's installed
(`.agents/skills/ui-sdk/SKILL.md`), and read
`node_modules/@ui-sdk/react/docs/llms.txt`. To build a new component, also
search `node_modules/@ui-sdk/react/docs/core.md` by heading, and read
`rendering.md` next to it.

## Structure

- `src/app/` is the shell: layout, navigation, routing, global styles.
- `src/shared/` holds code every module may use: `api/` (API client, shared
  lookups), `session/` (signed-in person, `useSession()`, name and password
  rules), `format/` (dates), `routing/`, `hooks/`, `ui/` (small UI pieces).
- `src/modules/<name>/` is one self-contained area of the panel (for example
  `sites`). A module exports a single `PanelModule` from its `index.ts` and is
  listed in `src/modules/registry.ts`. Modules never import from each other.
  `PanelModule.visible` hides a module (and its address) from people it
  doesn't apply to, for example administrator-only areas.

## Rules

- Text shown to people describes what happens for them. No library,
  protocol or implementation names in labels, titles, hints or messages.
- Passwords and token secrets are never written to storage or logs.
- The build output goes to `../internal/webui/dist`, which the Go daemon embeds.
