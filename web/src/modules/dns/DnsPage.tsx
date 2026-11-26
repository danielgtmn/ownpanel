import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { ownConnections, type Connection, type Zone } from '../../shared/dns/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { clearQuery, hrefFor, navigate, prefilledDomain, useQuery } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { PageHeader } from '../../shared/ui/index.ts'
import type { ModuleProps } from '../types.ts'
import { AddDomainDialog } from './AddDomainDialog.tsx'
import { adoptZone, createZone, listConnections, listZones } from './api.ts'
import { parseConnectionId } from './zones.ts'
import { ZonePage } from './ZonePage.tsx'
import { ZonesSection } from './ZonesSection.tsx'

function Overview() {
  const { user } = useSession()
  const connections = useResource(listConnections, 'dns-connections')
  const zones = useResource(listZones, 'dns-zones')
  const query = useQuery()
  const [adding, setAdding] = useState(false)
  usePageTitle('DNS')

  const accounts = connections.status === 'ready' ? ownConnections(connections.data, user) : null
  const open = adding || query.has('add')

  function close() {
    setAdding(false)
    clearQuery()
  }

  async function adopt(zone: Zone) {
    const adopted = await adoptZone(zone.connection_id, zone.name)
    close()
    toast.success(`${adopted.name} is now managed here.`)
    navigate('dns', String(adopted.connection_id), adopted.name)
  }

  async function create(connection: Connection, name: string): Promise<Zone> {
    const zone = await createZone(connection.id, name)
    toast.success(`${zone.name} was added to ${connection.name}.`)
    void zones.reload()
    return zone
  }

  return (
    <>
      <PageHeader
        title="DNS"
        description="Manage the DNS records of your domains, so visitors reach your sites and your email keeps working."
        actions={
          <Button data-variant="solid" onClick={() => setAdding(true)}>
            Add domain
          </Button>
        }
      />
      <ZonesSection zones={zones} hasAccounts={accounts === null || accounts.length > 0} onAdd={() => setAdding(true)} />
      <p className="muted dns-accounts-note">
        The accounts at your DNS provider are connected under <a href={hrefFor('settings')}>Settings</a>.
      </p>
      <AddDomainDialog
        open={open}
        accounts={accounts}
        domain={prefilledDomain(query)}
        onOpenChange={(next) => {
          if (!next) close()
        }}
        onAdopt={adopt}
        onCreate={create}
      />
    </>
  )
}

export function DnsPage({ segments }: ModuleProps) {
  const [id, zone] = segments
  if (!id || !zone) return <Overview />
  return <ZonePage key={`${id}/${zone}`} connectionId={parseConnectionId(id)} zone={zone} />
}
