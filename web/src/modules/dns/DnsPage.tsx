import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { PageHeader } from '../../shared/ui/index.ts'
import type { ModuleProps } from '../types.ts'
import { listConnections, listZones } from './api.ts'
import { ConnectionsSection } from './ConnectionsSection.tsx'
import { parseConnectionId } from './zones.ts'
import { ZonePage } from './ZonePage.tsx'
import { ZonesSection } from './ZonesSection.tsx'

function Overview() {
  const connections = useResource(listConnections, 'dns-connections')
  const zones = useResource(listZones, 'dns-zones')
  usePageTitle('DNS')

  async function changed() {
    await Promise.all([connections.reload(), zones.reload()])
  }

  return (
    <>
      <PageHeader
        title="DNS"
        description="Manage the DNS records of your domains, so visitors reach your sites and your email keeps working."
      />
      <ConnectionsSection connections={connections} onChanged={changed} />
      <ZonesSection zones={zones} hasConnections={connections.status === 'ready' && connections.data.length > 0} />
    </>
  )
}

export function DnsPage({ segments }: ModuleProps) {
  const [id, zone] = segments
  if (!id || !zone) return <Overview />
  return <ZonePage key={`${id}/${zone}`} connectionId={parseConnectionId(id)} zone={zone} />
}
