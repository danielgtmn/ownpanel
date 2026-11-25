import { Button } from '@ui-sdk/react/button'
import {
  createMenuController,
  Menu,
  MenuCheckboxItem,
  MenuContent,
  MenuContextTrigger,
  MenuGroup,
  MenuGroupLabel,
  MenuItem,
  MenuItemIcon,
  MenuItemIndicator,
  MenuRadioGroup,
  MenuRadioItem,
  MenuSeparator,
  MenuTrigger,
} from '@ui-sdk/react/menu'
import {
  NavigationMenu,
  NavigationMenuItem,
  NavigationMenuLink,
  NavigationMenuLinkIcon,
  NavigationMenuList,
} from '@ui-sdk/react/navigation-menu'
import { useShortcut, useShortcutHint } from '@ui-sdk/react/shortcuts'
import { createTooltipController, Tooltip, TooltipContent, TooltipTrigger } from '@ui-sdk/react/tooltip'
import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useState,
  useRef,
  type ReactElement,
  type ReactNode,
  type RefObject,
} from 'react'
import {
  hueOf,
  initialsOf,
  STATUS_TEXT,
  type Family,
  type FamilyRow,
  type FamilyStatus,
} from '../../shared/families/index.ts'
import { hrefWithQuery } from '../../shared/routing/index.ts'
import { ROLE_LABEL, useSession } from '../../shared/session/index.ts'
import {
  AppsIcon,
  CopyIcon,
  ExternalIcon,
  PanelCollapseIcon,
  PanelExpandIcon,
  PlusIcon,
  StackIcon,
} from '../../shared/ui/index.ts'
import {
  lookOf,
  updatePrefs,
  useRailPrefs,
  withExpanded,
  withInitialsForAll,
  withTileLook,
  type RailPrefs,
} from './railPrefs.ts'
import { ALL, familyHref, type RailEntry } from './selection.ts'
import { copyDomain, iconUrl, mainSite, openSite, siteAddress } from './tiles.ts'

type Tip = { title: string; detail?: string; shortcut?: string }

export type SidebarProps = {
  tile: string
  families: { family: Family; status: FamilyStatus }[]
  railEntries: RailEntry[]
  column: { title: string; summary: string; rows: FamilyRow[]; currentRow: string | null; placeholders: boolean }
  allHref: string
  onChangePassword: () => void
  onChangeEmail: () => void
  // The phone drawer always shows names next to the tiles.
  inDrawer?: boolean
}

const EXPAND_SHORTCUT = '['

const placeholders = [{ key: 'apps', label: 'Apps', icon: AppsIcon }]

function capitalize(text: string) {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

function TipTrigger({
  tips,
  tip,
  render,
  children,
}: {
  tips: ReturnType<typeof createTooltipController<Tip>>
  tip: Tip
  render: ReactElement
  children: ReactNode
}) {
  return (
    <TooltipTrigger controller={tips} payload={tip} render={render}>
      {children}
    </TooltipTrigger>
  )
}

// Whether the domain list has more above or below what is shown, for the faded edges.
// `content` changes when the list's first child is replaced, so the observer follows it.
function useScrollEdges(ref: RefObject<HTMLDivElement | null>, content: unknown) {
  const [edges, setEdges] = useState({ above: false, below: false })
  const measure = useCallback(() => {
    const el = ref.current
    if (!el) return
    const above = el.scrollTop > 1
    const below = el.scrollTop + el.clientHeight < el.scrollHeight - 1
    setEdges((current) => (current.above === above && current.below === below ? current : { above, below }))
  }, [ref])
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    if (el.firstElementChild) observer.observe(el.firstElementChild)
    return () => observer.disconnect()
  }, [ref, measure, content])
  return { edges, measure }
}

function TileLabel({ children }: { children: ReactNode }) {
  return (
    <span className="tile-label" aria-hidden="true">
      {children}
    </span>
  )
}

function FamilyFace({
  domain,
  status,
  src,
  onMissing,
}: {
  domain: string
  status: FamilyStatus
  src: string | null
  onMissing: (src: string) => void
}) {
  const [loaded, setLoaded] = useState<string | null>(null)
  const shown = src !== null && loaded === src
  return (
    <span className="tile-face" data-face={shown ? 'icon' : 'initials'} data-hue={hueOf(domain)}>
      {src ? (
        <img
          key={src}
          className="tile-icon"
          src={src}
          alt=""
          hidden={!shown}
          draggable={false}
          decoding="async"
          onLoad={() => setLoaded(src)}
          onError={() => onMissing(src)}
        />
      ) : null}
      {shown ? null : <span aria-hidden="true">{initialsOf(domain)}</span>}
      <span className="tile-dot" data-status={status} aria-hidden="true" />
    </span>
  )
}

function TileMenu({ family, prefs, iconMissing }: { family: Family; prefs: RailPrefs; iconMissing: boolean }) {
  const look = lookOf(prefs, family.domain)
  const address = siteAddress(family)
  return (
    <>
      <MenuGroup>
        <MenuGroupLabel>{family.domain}</MenuGroupLabel>
        <MenuRadioGroup
          value={look}
          onValueChange={(value) =>
            updatePrefs((current) => withTileLook(current, family.domain, value === 'icon' ? 'icon' : 'initials'))
          }
        >
          <MenuRadioItem value="icon" disabled={iconMissing}>
            <MenuItemIndicator />
            Show icon
            {iconMissing ? <span className="tile-menu-hint">None found</span> : null}
          </MenuRadioItem>
          <MenuRadioItem value="initials">
            <MenuItemIndicator />
            Show initials
          </MenuRadioItem>
        </MenuRadioGroup>
      </MenuGroup>
      <MenuSeparator />
      <MenuCheckboxItem
        value="initials-for-all"
        checked={prefs.initialsForAll}
        onCheckedChange={(checked) => updatePrefs((current) => withInitialsForAll(current, checked))}
      >
        <MenuItemIndicator />
        Show initials for all domains
      </MenuCheckboxItem>
      <MenuSeparator />
      <MenuItem disabled={!address} onSelect={() => void openSite(family)}>
        <MenuItemIcon>
          <ExternalIcon />
        </MenuItemIcon>
        Open site
      </MenuItem>
      <MenuItem onSelect={() => void copyDomain(family)}>
        <MenuItemIcon>
          <CopyIcon />
        </MenuItemIcon>
        Copy domain
      </MenuItem>
    </>
  )
}

function Rail({
  tile,
  families,
  railEntries,
  allHref,
  onChangePassword,
  onChangeEmail,
  inDrawer = false,
}: SidebarProps) {
  const [tips] = useState(() => createTooltipController<Tip>())
  const [tileMenu] = useState(() => createMenuController<Family>())
  const [missing, setMissing] = useState<ReadonlySet<string>>(() => new Set())
  const { user, signOut, signOutEverywhere } = useSession()
  const prefs = useRailPrefs()
  const railId = useId()
  const expanded = inDrawer || prefs.expanded
  const toggle = useCallback(() => updatePrefs((current) => withExpanded(current, !current.expanded)), [])
  useShortcut(inDrawer ? null : EXPAND_SHORTCUT, toggle, { description: 'Show or hide names in the sidebar' })
  const hint = useShortcutHint(EXPAND_SHORTCUT)
  const markMissing = useCallback(
    (src: string) => setMissing((current) => (current.has(src) ? current : new Set(current).add(src))),
    [],
  )
  const scrollRef = useRef<HTMLDivElement>(null)
  const { edges, measure } = useScrollEdges(scrollRef, families.length > 0)
  // Keep the selected domain in view, e.g. after opening its page by its address.
  useEffect(() => {
    scrollRef.current?.querySelector('[data-current]')?.scrollIntoView({ block: 'nearest' })
  }, [tile, families.length])
  const iconOf = (family: Family) => {
    const site = mainSite(family)
    return site ? iconUrl(site) : null
  }

  return (
    <div className="rail" id={railId} data-expanded={expanded ? '' : undefined}>
      <div className="rail-top">
        <NavigationMenu aria-label="Overview" orientation="vertical" className="rail-nav">
          <NavigationMenuList>
            <NavigationMenuItem>
              <TipTrigger
                tips={tips}
                tip={{ title: 'All domains', detail: 'Sites, DNS, redirects and e-mail' }}
                render={
                  <NavigationMenuLink
                    href={allHref}
                    current={tile === ALL}
                    aria-label="All domains"
                    className="tile"
                    data-kind="all"
                  />
                }
              >
                <span className="tile-face">
                  <StackIcon />
                </span>
                <TileLabel>All domains</TileLabel>
              </TipTrigger>
            </NavigationMenuItem>
          </NavigationMenuList>
        </NavigationMenu>

        <Menu>
          <TipTrigger
            tips={tips}
            tip={{ title: 'Add', detail: 'A site, a domain, a redirect or e-mail' }}
            render={
              <MenuTrigger className="tile" data-kind="add" aria-label="Add a site, domain, redirect or e-mail" />
            }
          >
            <span className="tile-face">
              <PlusIcon />
            </span>
            <TileLabel>Add</TileLabel>
          </TipTrigger>
          <MenuContent side="right" align="start">
            <MenuItem render={<a href={hrefWithQuery(['sites'], { new: '1' })} />}>New site</MenuItem>
            <MenuItem render={<a href={hrefWithQuery(['dns'], { add: '1' })} />}>Add a domain</MenuItem>
            <MenuItem render={<a href={hrefWithQuery(['redirects'], { new: '1' })} />}>New redirect</MenuItem>
            <MenuItem render={<a href={hrefWithQuery(['mail'], { new: '1' })} />}>Turn on e-mail for a domain</MenuItem>
          </MenuContent>
        </Menu>
      </div>

      <div
        ref={scrollRef}
        className="rail-scroll"
        data-more-above={edges.above ? '' : undefined}
        data-more-below={edges.below ? '' : undefined}
        onScroll={measure}
      >
        {families.length > 0 ? (
          <NavigationMenu aria-label="Domains" orientation="vertical" className="rail-nav">
            <NavigationMenuList>
              {families.map(({ family, status }) => {
                const src = iconOf(family)
                return (
                  <NavigationMenuItem key={family.domain}>
                    <TipTrigger
                      tips={tips}
                      tip={{ title: family.domain, detail: capitalize(STATUS_TEXT[status]) }}
                      render={
                        <MenuContextTrigger
                          controller={tileMenu}
                          payload={family}
                          render={
                            <NavigationMenuLink
                              href={familyHref(family.domain)}
                              current={tile === family.domain}
                              aria-label={`${family.domain}, ${STATUS_TEXT[status]}`}
                              className="tile"
                              data-kind="family"
                              onKeyDown={(event) => {
                                // Browsers on macOS send no context menu event for these keys.
                                if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
                                  event.preventDefault()
                                  void tileMenu.open({ payload: family, anchor: event.currentTarget, focus: 'first' })
                                }
                              }}
                            />
                          }
                        />
                      }
                    >
                      <FamilyFace
                        domain={family.domain}
                        status={status}
                        src={lookOf(prefs, family.domain) === 'icon' && src && !missing.has(src) ? src : null}
                        onMissing={markMissing}
                      />
                      <TileLabel>{family.domain}</TileLabel>
                    </TipTrigger>
                  </NavigationMenuItem>
                )
              })}
            </NavigationMenuList>
          </NavigationMenu>
        ) : null}
      </div>

      <Menu controller={tileMenu} onOpenChange={(open) => open && tips.close()}>
        <MenuContent<Family> side="right" align="start" className="tile-menu">
          {(state) => {
            const family = state.payload
            if (!family) return null
            const src = iconOf(family)
            return <TileMenu family={family} prefs={prefs} iconMissing={!src || missing.has(src)} />
          }}
        </MenuContent>
      </Menu>

      <div className="rail-bottom">
        <NavigationMenu aria-label="Panel" orientation="vertical" className="rail-nav">
          <NavigationMenuList>
            {railEntries.map((entry) => (
              <NavigationMenuItem key={entry.key}>
                <TipTrigger
                  tips={tips}
                  tip={{ title: entry.label }}
                  render={
                    <NavigationMenuLink
                      href={entry.href}
                      current={entry.current}
                      aria-label={entry.label}
                      className="tile"
                      data-kind="entry"
                    />
                  }
                >
                  <span className="tile-face">
                    <entry.icon />
                  </span>
                  <TileLabel>{entry.label}</TileLabel>
                </TipTrigger>
              </NavigationMenuItem>
            ))}
          </NavigationMenuList>
        </NavigationMenu>

        <Menu>
          <TipTrigger
            tips={tips}
            tip={{ title: user.name, detail: 'Account' }}
            render={<MenuTrigger className="tile" data-kind="account" aria-label={`Account of ${user.name}`} />}
          >
            <span className="tile-face">
              <span aria-hidden="true">{initialsOf(user.name)}</span>
            </span>
            <TileLabel>{user.name}</TileLabel>
          </TipTrigger>
          <MenuContent side="right" align="end">
            <MenuItem onSelect={onChangePassword}>Change password</MenuItem>
            <MenuItem onSelect={onChangeEmail}>E-mail address</MenuItem>
            <MenuItem onSelect={signOut}>Sign out</MenuItem>
            <MenuItem onSelect={signOutEverywhere}>Sign out everywhere</MenuItem>
          </MenuContent>
        </Menu>

        {inDrawer ? null : (
          <TipTrigger
            tips={tips}
            tip={{ title: expanded ? 'Hide names' : 'Show names', shortcut: hint.text }}
            render={
              <Button
                className="tile"
                data-kind="toggle"
                aria-expanded={expanded}
                aria-controls={railId}
                aria-keyshortcuts={hint.ariaKeyshortcuts}
                onClick={toggle}
              />
            }
          >
            <span className="tile-face">{expanded ? <PanelCollapseIcon /> : <PanelExpandIcon />}</span>
            <span className="tile-label">{expanded ? 'Hide names' : 'Show names'}</span>
          </TipTrigger>
        )}
      </div>

      <Tooltip controller={tips}>
        <TooltipContent<Tip> side="right" sideOffset={10} className="rail-tip">
          {(state) =>
            state.payload ? (
              <>
                <span className="rail-tip-title">
                  {state.payload.title}
                  {state.payload.shortcut ? (
                    <kbd className="rail-tip-key" aria-hidden="true">
                      {state.payload.shortcut}
                    </kbd>
                  ) : null}
                </span>
                {state.payload.detail ? <span className="rail-tip-detail">{state.payload.detail}</span> : null}
              </>
            ) : null
          }
        </TooltipContent>
      </Tooltip>
    </div>
  )
}

function Column({ column }: SidebarProps) {
  const { user } = useSession()
  return (
    <div className="column">
      <header className="column-head">
        <strong className="column-title">{column.title}</strong>
        <span className="column-summary">{column.summary}</span>
      </header>
      <NavigationMenu aria-label={`Pages of ${column.title}`} orientation="vertical" className="column-nav">
        <NavigationMenuList>
          {column.rows.map((row) => (
            <NavigationMenuItem key={row.key}>
              <NavigationMenuLink href={row.href} current={column.currentRow === row.key} className="nav-row">
                <NavigationMenuLinkIcon>
                  <row.icon />
                </NavigationMenuLinkIcon>
                <span className="nav-row-text">
                  <span className="nav-row-label">{row.label}</span>
                  {row.hint ? (
                    <span className="nav-row-hint">
                      <span className="visually-hidden">, </span>
                      {row.hint}
                    </span>
                  ) : null}
                </span>
                {row.detail ? <span className="nav-row-count">{row.detail}</span> : null}
                {row.status ? (
                  <>
                    <span className="nav-row-dot" data-status={row.status} aria-hidden="true" />
                    <span className="visually-hidden">, {row.statusText}</span>
                  </>
                ) : null}
              </NavigationMenuLink>
            </NavigationMenuItem>
          ))}
        </NavigationMenuList>
      </NavigationMenu>
      {column.placeholders ? (
        <ul className="column-later" aria-label="Coming later">
          {placeholders.map((item) => (
            <li key={item.key} className="nav-row" aria-disabled="true">
              <span className="nav-row-icon">
                <item.icon />
              </span>
              <span className="nav-row-text">
                <span className="nav-row-label">{item.label}</span>
              </span>
              <span className="nav-row-count">Coming later</span>
            </li>
          ))}
        </ul>
      ) : null}
      <footer className="column-status">
        <span className="column-status-dot" aria-hidden="true" />
        <span>
          Signed in as {user.name} · {ROLE_LABEL[user.role]}
        </span>
      </footer>
    </div>
  )
}

export function Sidebar(props: SidebarProps) {
  return (
    <div className="sidebar" data-in-drawer={props.inDrawer ? '' : undefined}>
      <Rail {...props} />
      <Column {...props} />
    </div>
  )
}
