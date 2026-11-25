import './redirects.css'
import type { Family, FamilyRow } from '../../shared/families/index.ts'
import { hrefWithQuery } from '../../shared/routing/index.ts'
import { RedirectIcon } from '../../shared/ui/index.ts'
import { RedirectsPage } from './RedirectsPage.tsx'
import type { PanelModule } from '../types.ts'

export function redirectRows(family: Family): FamilyRow[] {
  return [
    {
      key: 'redirects',
      label: 'Redirects',
      detail: String(family.redirects.length),
      href: hrefWithQuery(['redirects'], { family: family.domain }),
      icon: RedirectIcon,
      isCurrent: (route) => route.segments[0] === 'redirects' && route.query.get('family') === family.domain,
    },
  ]
}

export const redirectsModule: PanelModule = {
  id: 'redirects',
  label: 'Redirects',
  basePath: '/redirects',
  component: RedirectsPage,
  overviewRow: { icon: RedirectIcon, count: (sources) => sources.redirects.length },
  familyRows: redirectRows,
}
