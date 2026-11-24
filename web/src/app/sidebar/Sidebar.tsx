import { Menu, MenuContent, MenuItem, MenuTrigger } from '@ui-sdk/react/menu'
import {
  NavigationMenu,
  NavigationMenuItem,
  NavigationMenuLink,
  NavigationMenuLinkIcon,
  NavigationMenuList,
} from '@ui-sdk/react/navigation-menu'
import { createTooltipController, Tooltip, TooltipContent, TooltipTrigger } from '@ui-sdk/react/tooltip'
import { useState, type ReactElement, type ReactNode } from 'react'
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
import { AppsIcon, MailIcon, PlusIcon, StackIcon } from '../../shared/ui/index.ts'
import { ALL, familyHref, type RailEntry } from './selection.ts'

type Tip = { title: string; detail?: string }


export type SidebarProps = {
  tile: string
  families: { family: Family; status: FamilyStatus }[]
  railEntries: RailEntry[]
  column: { title: string; summary: string; rows: FamilyRow[]; currentRow: string | null; placeholders: boolean }
  allHref: string
  onChangePassword: () => void
}

const placeholders = [
  { key: 'mail', label: 'E-mail', icon: MailIcon },
  { key: 'apps', label: 'Apps', icon: AppsIcon },
]

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

function Rail({ tile, families, railEntries, allHref, onChangePassword }: SidebarProps) {
  const [tips] = useState(() => createTooltipController<Tip>())
  const { user, signOut } = useSession()

  return (
    <div className="rail">
      <NavigationMenu aria-label="Domains" orientation="vertical" className="rail-nav">
        <NavigationMenuList>
          <NavigationMenuItem>
            <TipTrigger
              tips={tips}
              tip={{ title: 'All domains', detail: 'Sites, DNS and redirects' }}
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
              <StackIcon />
            </TipTrigger>
          </NavigationMenuItem>
          {families.map(({ family, status }) => (
            <NavigationMenuItem key={family.domain}>
              <TipTrigger
                tips={tips}
                tip={{ title: family.domain, detail: capitalize(STATUS_TEXT[status]) }}
                render={
                  <NavigationMenuLink
                    href={familyHref(family.domain)}
                    current={tile === family.domain}
                    aria-label={`${family.domain}, ${STATUS_TEXT[status]}`}
                    className="tile"
                    data-kind="family"
                    data-hue={hueOf(family.domain)}
                  />
                }
              >
                <span aria-hidden="true">{initialsOf(family.domain)}</span>
                <span className="tile-dot" data-status={status} aria-hidden="true" />
              </TipTrigger>
            </NavigationMenuItem>
          ))}
        </NavigationMenuList>
      </NavigationMenu>

      <Menu>
        <TipTrigger
          tips={tips}
          tip={{ title: 'Add', detail: 'A site, a domain or a redirect' }}
          render={<MenuTrigger className="tile" data-kind="add" aria-label="Add a site, domain or redirect" />}
        >
          <PlusIcon />
        </TipTrigger>
        <MenuContent side="right" align="start">
          <MenuItem render={<a href={hrefWithQuery(['sites'], { new: '1' })} />}>New site</MenuItem>
          <MenuItem render={<a href={hrefWithQuery(['dns'], { add: '1' })} />}>Add a domain at your DNS provider</MenuItem>
          <MenuItem render={<a href={hrefWithQuery(['redirects'], { new: '1' })} />}>New redirect</MenuItem>
        </MenuContent>
      </Menu>

      <div className="rail-grow" />

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
                <entry.icon />
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
          <span aria-hidden="true">{initialsOf(user.name)}</span>
        </TipTrigger>
        <MenuContent side="right" align="end">
          <MenuItem onSelect={onChangePassword}>Change password</MenuItem>
          <MenuItem onSelect={signOut}>Sign out</MenuItem>
        </MenuContent>
      </Menu>

      <Tooltip controller={tips}>
        <TooltipContent<Tip> side="right" sideOffset={10} className="rail-tip">
          {(state) =>
            state.payload ? (
              <>
                <span className="rail-tip-title">{state.payload.title}</span>
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
    <div className="sidebar">
      <Rail {...props} />
      <Column {...props} />
    </div>
  )
}
