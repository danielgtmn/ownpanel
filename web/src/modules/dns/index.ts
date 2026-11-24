import './dns.css'
import { startsWith, type Family, type FamilyRow } from '../../shared/families/index.ts'
import { hrefFor } from '../../shared/routing/index.ts'
import { PlugIcon, RecordsIcon } from '../../shared/ui/index.ts'
import { DnsPage } from './DnsPage.tsx'
import type { PanelModule } from '../types.ts'

export function zoneRows(family: Family): FamilyRow[] {
  return family.zones.map((zone) => ({
    key: `zone:${zone.connectionId}/${zone.name}`,
    label: 'DNS records',
    hint: family.zones.length > 1 ? zone.connectionName : undefined,
    href: hrefFor('dns', String(zone.connectionId), zone.name),
    icon: RecordsIcon,
    isCurrent: (route) => startsWith(route.segments, ['dns', String(zone.connectionId), zone.name]),
  }))
}

export const dnsModule: PanelModule = {
  id: 'dns',
  label: 'DNS',
  basePath: '/dns',
  component: DnsPage,
  railEntry: { label: 'DNS connections', icon: PlugIcon },
  overviewRow: { label: 'DNS', icon: RecordsIcon, count: (sources) => sources.zones.length },
  familyRows: zoneRows,
}
