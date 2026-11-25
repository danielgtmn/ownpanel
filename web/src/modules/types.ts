import type { ComponentType } from 'react'
import type { DomainSources, Family, FamilyRow } from '../shared/families/index.ts'
import type { User } from '../shared/session/index.ts'

export type ModuleProps = {
  segments: string[]
}

export type PanelModule = {
  id: string
  label: string
  basePath: string
  // Loads the page on demand, so its code stays out of the first download:
  // `() => import('./SitesPage.tsx').then((page) => page.SitesPage)`.
  // Everything else here is read by the sidebar and stays synchronous.
  page: () => Promise<ComponentType<ModuleProps>>
  visible?: (user: User) => boolean
  // An icon tile in the rail's bottom group.
  railEntry?: { label?: string; icon: ComponentType }
  // A row in the column of the "All" tile, with a count from everyone's domain data.
  overviewRow?: { label?: string; icon: ComponentType; count: (sources: DomainSources) => number }
  // Rows in the column of one domain family.
  familyRows?: (family: Family) => FamilyRow[]
}
