import { Drawer, DrawerBackdrop, DrawerContent, DrawerTitle, DrawerTrigger } from '@ui-sdk/react/drawer'
import { useEffect, useMemo, useRef, useState } from 'react'
import { modules } from '../modules/registry.ts'
import {
  familyStatus,
  familySummary,
  findFamily,
  overviewSummary,
  type Family,
} from '../shared/families/index.ts'
import { useMediaQuery } from '../shared/hooks/index.ts'
import { hrefFor, navigate, useRoute } from '../shared/routing/index.ts'
import { useSession } from '../shared/session/index.ts'
import { EmptyState, MenuIcon } from '../shared/ui/index.ts'
import { ChangePasswordDialog } from './ChangePasswordDialog.tsx'
import { EmailAddressDialog } from './EmailAddressDialog.tsx'
import { defaultModule, moduleFor, visibleModules } from './modules.ts'
import { lazyPage } from './lazyPage.ts'
import { ModulePage, PageFrame } from './ModulePage.tsx'
import {
  ALL,
  currentRow,
  familyFromRoute,
  familyRows,
  overviewRows,
  railEntries,
  selectTile,
} from './sidebar/selection.ts'
import { Sidebar, type SidebarProps } from './sidebar/Sidebar.tsx'
import './sidebar/sidebar.css'
import { useDomainData, type DomainData } from './sidebar/useDomainData.ts'

// The overview of one domain family is a page of its own, loaded when it's opened.
const FamilyPage = lazyPage<{ domain: string; data: DomainData; allHref: string }>(() =>
  import('./sidebar/FamilyPage.tsx').then((page) => page.FamilyPage),
)

export function Shell() {
  const route = useRoute()
  const { segments } = route
  const { user } = useSession()
  const available = useMemo(() => visibleModules(modules, user), [user])
  const data = useDomainData()
  const phone = useMediaQuery('(max-width: 760px)')
  const [menuOpen, setMenuOpen] = useState(false)
  const [changingPassword, setChangingPassword] = useState(false)
  const [changingEmail, setChangingEmail] = useState(false)
  const [lastTile, setLastTile] = useState(ALL)
  const mainRef = useRef<HTMLElement>(null)
  const firstRender = useRef(true)
  const familyDomain = familyFromRoute(route)
  const match = familyDomain ? null : moduleFor(available, segments)
  const routeKey = segments.join('/') + '?' + route.query.toString()
  const first = defaultModule(available)
  const allHref = first ? hrefFor(first.basePath.replace(/^\//, '')) : hrefFor()

  const families = useMemo(() => (data.status === 'ready' ? data.families : []), [data])
  const sources = data.status === 'ready' ? data.sources : null
  const rowsOf = (family: Family) => familyRows(available, family)
  const overview = overviewRows(available, (module) => (sources ? (module.overviewRow?.count(sources) ?? null) : null))
  const tile = selectTile(route, families, rowsOf, overview, lastTile)
  const selected = tile === ALL ? undefined : findFamily(families, tile)
  if (tile !== lastTile) setLastTile(tile)
  // Following a link in the drawer closes it.
  const [shownRoute, setShownRoute] = useState(routeKey)
  if (shownRoute !== routeKey) {
    setShownRoute(routeKey)
    setMenuOpen(false)
  }

  useEffect(() => {
    if (!match && !familyDomain && segments.length === 0 && first) navigate(first.basePath.replace(/^\//, ''))
  })

  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false
      return
    }
    mainRef.current?.focus()
  }, [routeKey])

  const rows = tile === ALL ? overview : selected ? rowsOf(selected) : []
  const rowKey = currentRow(rows, route)
  const states = data.status === 'ready' ? data.states : {}

  const sidebar: SidebarProps = {
    tile,
    allHref,
    families: families.map((family) => ({ family, status: familyStatus(family, states) })),
    railEntries: railEntries(available, match?.module.id ?? null, rowKey),
    column: {
      title: tile === ALL ? 'All domains' : tile,
      summary:
        tile === ALL
          ? sources
            ? overviewSummary(sources, families)
            : 'Loading…'
          : selected
            ? familySummary(selected)
            : data.status === 'loading'
              ? 'Loading…'
              : 'Not used here',
      rows,
      currentRow: rowKey,
      placeholders: tile !== ALL && selected !== undefined,
    },
    onChangePassword: () => setChangingPassword(true),
    onChangeEmail: () => setChangingEmail(true),
  }

  const content = familyDomain ? (
    <PageFrame key={familyDomain}>
      <FamilyPage domain={familyDomain} data={data} allHref={allHref} />
    </PageFrame>
  ) : match ? (
    <ModulePage key={match.module.id} module={match.module} segments={match.rest} />
  ) : segments.length > 0 ? (
    <EmptyState
      title="Page not found"
      description="This page doesn't exist."
      action={<a href={allHref}>Go to the start page</a>}
    />
  ) : null

  return (
    <div className="shell" data-layout={phone ? 'phone' : 'wide'}>
      {phone ? (
        <header className="topbar">
          <Drawer side="left" open={menuOpen} onOpenChange={setMenuOpen}>
            <DrawerTrigger className="topbar-menu" aria-label="Open navigation">
              <MenuIcon />
            </DrawerTrigger>
            <DrawerBackdrop className="nav-drawer-backdrop" />
            <DrawerContent className="nav-drawer">
              <DrawerTitle className="visually-hidden">Navigation</DrawerTitle>
              <Sidebar {...sidebar} inDrawer />
            </DrawerContent>
          </Drawer>
          <a className="brand" href={allHref}>
            ownpanel
          </a>
          <span className="topbar-context">{sidebar.column.title}</span>
        </header>
      ) : (
        <div className="shell-side">
          <Sidebar {...sidebar} />
        </div>
      )}
      <main className="shell-main" ref={mainRef} tabIndex={-1}>
        <div className="shell-content">{content}</div>
      </main>
      <ChangePasswordDialog open={changingPassword} onOpenChange={setChangingPassword} />
      <EmailAddressDialog open={changingEmail} onOpenChange={setChangingEmail} />
    </div>
  )
}
