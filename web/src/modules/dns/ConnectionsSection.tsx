import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useEffect, useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDate, formatDateTime, isoTimestamp } from '../../shared/format/index.ts'
import type { Resource } from '../../shared/hooks/index.ts'
import { clearQuery, useQuery } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { EmptyState, ErrorState, LoadingState, Section } from '../../shared/ui/index.ts'
import { AddDomainDialog } from './AddDomainDialog.tsx'
import { checkConnection, createConnection, createZone, deleteConnection } from './api.ts'
import { ConnectDialog } from './ConnectDialog.tsx'
import type { Connection, NewConnection, Zone } from './types.ts'
import { connectionHealth, isOwnConnection, providerLabel, zoneCountLabel } from './zones.ts'

type RowProps = {
  connection: Connection
  own: boolean
  onCheck: (connection: Connection) => Promise<void>
  onRemove: (connection: Connection) => Promise<void>
  onAddDomain: (connection: Connection) => void
}

function ConnectionRow({ connection, own, onCheck, onRemove, onAddDomain }: RowProps) {
  const health = connectionHealth(connection, formatDateTime)
  const count = zoneCountLabel(connection.zone_count)
  return (
    <li className="record-row">
      <div className="record-main">
        <h3 className="record-title">{connection.name}</h3>
        <ul className="record-facts">
          <li>{providerLabel(connection.provider)}</li>
          {own ? null : <li>Belongs to {connection.owner}</li>}
          <li>
            Added <time dateTime={isoTimestamp(connection.created_at)}>{formatDate(connection.created_at)}</time>
          </li>
          {count ? <li>{count}</li> : null}
        </ul>
        <p className="connection-health" data-tone={health.tone}>
          {health.text}
        </p>
        {own && connection.can_write === false ? (
          <p className="muted connection-note">
            This API token can only read. Connect again with a “Read & Write” token to change records.
          </p>
        ) : null}
      </div>
      <div className="record-actions">
        {own ? (
          <Button onClick={() => onAddDomain(connection)}>
            Add domain<span className="visually-hidden"> to {connection.name}</span>
          </Button>
        ) : null}
        {own ? (
          <Button pendingAnnouncement="Checking" onClick={() => onCheck(connection)}>
            Check again<span className="visually-hidden"> for {connection.name}</span>
          </Button>
        ) : null}
        <Button data-variant="destructive" pendingAnnouncement="Removing" onClick={() => onRemove(connection)}>
          Remove<span className="visually-hidden"> {connection.name}</span>
        </Button>
      </div>
    </li>
  )
}

type Props = {
  connections: Resource<Connection[]>
  onChanged: () => Promise<void>
}

export function ConnectionsSection({ connections, onChanged }: Props) {
  const { user } = useSession()
  const [connecting, setConnecting] = useState(false)
  const [addingTo, setAddingTo] = useState<Connection | null>(null)
  const wantsAdd = useQuery().has('add')
  const ready = connections.status === 'ready' ? connections.data : null

  // "Add a domain" from the sidebar: with one connection of your own, open its dialog; with none, offer to connect.
  const own = wantsAdd && ready ? ready.filter((connection) => isOwnConnection(connection, user)) : null
  const askedToConnect = own?.length === 0
  const askedToAdd = own?.length === 1 ? own[0] : null
  const askedToChoose = (own?.length ?? 0) > 1

  useEffect(() => {
    if (!askedToChoose) return
    toast.info('Choose the connection to add the domain to.')
    clearQuery()
  }, [askedToChoose])

  async function addDomain(connection: Connection, name: string): Promise<Zone> {
    const zone = await createZone(connection.id, name)
    toast.success(`${zone.name} was added to ${connection.name}.`)
    void onChanged()
    return zone
  }

  async function create(connection: NewConnection) {
    const created = await createConnection(connection)
    setConnecting(false)
    clearQuery()
    toast.success(`${created.name} is connected.`)
    await onChanged()
  }

  async function check(connection: Connection) {
    try {
      const result = await checkConnection(connection.id)
      if (result.last_error.trim()) toast.warning(`${connection.name}: ${result.last_error.trim()}`)
      else toast.success(`${connection.name} is working.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await onChanged()
  }

  async function remove(connection: Connection) {
    const yes = await confirm({
      title: `Remove ${connection.name}?`,
      description:
        "You can't manage its domains here anymore until you connect again. DNS records at Hetzner are not changed by removing the connection.",
      confirmLabel: 'Remove connection',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteConnection(connection.id)
      toast.success(`${connection.name} was removed.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await onChanged()
  }

  const connectButton = (
    <Button data-variant="solid" onClick={() => setConnecting(true)}>
      Connect Hetzner
    </Button>
  )

  return (
    <Section
      title="Connections"
      description="Connect the company that manages your domains, so you can change their DNS records here."
      actions={connections.status === 'ready' && connections.data.length > 0 ? connectButton : null}
    >
      {connections.status === 'loading' ? <LoadingState label="Loading your connections…" /> : null}
      {connections.status === 'error' ? (
        <ErrorState
          title="Couldn't load your connections"
          message={errorMessage(connections.error)}
          onRetry={connections.retry}
        />
      ) : null}
      {connections.status === 'ready' && connections.data.length === 0 ? (
        <EmptyState
          title="Nothing connected yet"
          description="Connect Hetzner to see your domains and manage their DNS records."
          action={connectButton}
        />
      ) : null}
      {connections.status === 'ready' && connections.data.length > 0 ? (
        <ul className="record-list" aria-label="Connections">
          {connections.data.map((connection) => (
            <ConnectionRow
              key={connection.id}
              connection={connection}
              own={isOwnConnection(connection, user)}
              onCheck={check}
              onRemove={remove}
              onAddDomain={setAddingTo}
            />
          ))}
        </ul>
      ) : null}
      <ConnectDialog
        open={connecting || askedToConnect}
        onOpenChange={(open) => {
          setConnecting(open)
          if (!open) clearQuery()
        }}
        onSubmit={create}
      />
      <AddDomainDialog
        connection={addingTo ?? askedToAdd}
        onOpenChange={(open) => {
          if (open) return
          setAddingTo(null)
          clearQuery()
        }}
        onCreate={addDomain}
      />
    </Section>
  )
}
