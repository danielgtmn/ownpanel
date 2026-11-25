import './sites.css'
import { startsWith, type Family, type FamilyRow } from '../../shared/families/index.ts'
import { hrefFor } from '../../shared/routing/index.ts'
import { GlobeIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export function siteRows(family: Family): FamilyRow[] {
  return family.sites.map((site) => ({
    key: `site:${site.name}`,
    label: site.name,
    hint: site.otherFamilies.length > 0 ? `Website, also on ${site.otherFamilies.join(', ')}` : 'Website',
    href: hrefFor('sites', site.name),
    icon: GlobeIcon,
    status: site.live ? 'live' : 'attention',
    statusText: site.live ? 'Live' : 'No content yet',
    isCurrent: (route) => startsWith(route.segments, ['sites', site.name]),
  }))
}

export const sitesModule: PanelModule = {
  id: 'sites',
  label: 'Sites',
  basePath: '/sites',
  page: () => import('./SitesPage.tsx').then((page) => page.SitesPage),
  overviewRow: { icon: GlobeIcon, count: (sources) => sources.sites.length },
  familyRows: siteRows,
}
