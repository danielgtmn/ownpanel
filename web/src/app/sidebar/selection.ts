import type { ComponentType } from 'react'
import type { PanelModule } from '../../modules/types.ts'
import { normalizeHost, startsWith, type Family, type FamilyRow } from '../../shared/families/index.ts'
import { hrefFor, type Route } from '../../shared/routing/index.ts'
import { OverviewIcon } from '../../shared/ui/index.ts'

export const ALL = 'all'
export const FAMILY_SEGMENT = 'd'

export function familyHref(domain: string): string {
  return hrefFor(FAMILY_SEGMENT, domain)
}

export function familyFromRoute(route: Route): string | null {
  if (route.segments[0] !== FAMILY_SEGMENT) return null
  return normalizeHost(route.segments[1] ?? '') || null
}

export function familyRows(modules: PanelModule[], family: Family): FamilyRow[] {
  const overview: FamilyRow = {
    key: 'family-overview',
    label: 'Overview',
    href: familyHref(family.domain),
    icon: OverviewIcon,
    isCurrent: (route) => familyFromRoute(route) === family.domain,
  }
  return [overview, ...modules.flatMap((module) => module.familyRows?.(family) ?? [])]
}

export function overviewRows(modules: PanelModule[], counts: (module: PanelModule) => number | null): FamilyRow[] {
  return modules.flatMap((module) => {
    const row = module.overviewRow
    if (!row) return []
    const base = module.basePath.replace(/^\//, '')
    const count = counts(module)
    return [
      {
        key: `overview:${module.id}`,
        label: row.label ?? module.label,
        href: hrefFor(base),
        icon: row.icon,
        detail: count === null ? undefined : String(count),
        isCurrent: (route: Route) => startsWith(route.segments, [base]) && !route.query.has('family'),
      },
    ]
  })
}

// Which rail tile a route belongs to. A family page names its tile; any other page goes to the
// family that has a row for it, preferring the one already open, else to "All". Pages no column
// lists, like Settings, keep the open tile.
export function selectTile(
  route: Route,
  families: Family[],
  rowsOf: (family: Family) => FamilyRow[],
  overview: FamilyRow[],
  previous: string,
): string {
  const named = familyFromRoute(route)
  if (named) return named
  const owns = (family: Family) => rowsOf(family).some((row) => row.isCurrent(route))
  const kept = families.find((family) => family.domain === previous)
  if (kept && owns(kept)) return kept.domain
  const owner = families.find(owns)
  if (owner) return owner.domain
  if (overview.some((row) => row.isCurrent(route))) return ALL
  return kept ? kept.domain : ALL
}

export type RailEntry = { key: string; label: string; href: string; icon: ComponentType; current: boolean }

// A rail entry is current when its page is open and no column row already marks it.
export function railEntries(modules: PanelModule[], currentModule: string | null, rowKey: string | null): RailEntry[] {
  return modules.flatMap((module) =>
    module.railEntry
      ? [
          {
            key: module.id,
            label: module.railEntry.label ?? module.label,
            href: hrefFor(module.basePath.replace(/^\//, '')),
            icon: module.railEntry.icon,
            current: currentModule === module.id && rowKey === null,
          },
        ]
      : [],
  )
}

export function currentRow(rows: FamilyRow[], route: Route): string | null {
  return rows.find((row) => row.isCurrent(route))?.key ?? null
}
