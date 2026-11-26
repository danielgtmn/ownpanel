import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import {
  checkConnection,
  connectionHealth,
  createConnection,
  deleteConnection,
  isOwnConnection,
  listConnections,
  providerLabel,
  zoneCountLabel,
  type Connection,
  type NewConnection,
} from '../../shared/dns/index.ts'
import { formatDate, formatDateTime, isoTimestamp } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { addDomainHref, clearQuery, useQuery } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { EmptyState, ErrorState, LoadingState, Section } from '../../shared/ui/index.ts'
import { ConnectDialog } from './ConnectDialog.tsx'

type RowProps = {
  connection: Connection
  own: boolean
  onCheck: (connection: Connection) => Promise<void>
  onRemove: (connection: Connection) => Promise<void>
}

function AccountRow({ connection, own, onCheck, onRemove }: RowProps) {
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
          {count ? <li>{count} in the account</li> : null}
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

/** The accounts at DNS providers. Connecting one only gives access; its domains are chosen under DNS. */
export function ConnectedAccountsSection() {
  const { user } = useSession()
  const connections = useResource(listConnections, 'dns-connections')
  const wantsConnect = useQuery().has('connect')
  const [connecting, setConnecting] = useState(false)

  async function create(connection: NewConnection) {
    const created = await createConnection(connection)
    setConnecting(false)
    clearQuery()
    toast.success(`${created.name} is connected. Choose under DNS which of its domains to manage here.`, {
      action: { label: 'Choose domains', onAction: () => (window.location.hash = addDomainHref()) },
    })
    await connections.reload()
  }

  async function check(connection: Connection) {
    try {
      const result = await checkConnection(connection.id)
      if (result.last_error.trim()) toast.warning(`${connection.name}: ${result.last_error.trim()}`)
      else toast.success(`${connection.name} is working.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await connections.reload()
  }

  async function remove(connection: Connection) {
    const yes = await confirm({
      title: `Remove ${connection.name}?`,
      description:
        "Its domains are no longer managed here until you connect it again. Nothing at Hetzner is changed or deleted.",
      confirmLabel: 'Remove account',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteConnection(connection.id)
      toast.success(`${connection.name} was removed.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await connections.reload()
  }

  const connectButton = (
    <Button data-variant="solid" onClick={() => setConnecting(true)}>
      Connect Hetzner
    </Button>
  )

  return (
    <Section
      title="Connected accounts"
      description="Accounts at the company that manages your domains. Connecting one gives the panel access; you choose under DNS which of its domains to manage here."
      actions={connections.status === 'ready' && connections.data.length > 0 ? connectButton : null}
    >
      {connections.status === 'loading' ? <LoadingState label="Loading your connected accounts…" /> : null}
      {connections.status === 'error' ? (
        <ErrorState
          title="Couldn't load your connected accounts"
          message={errorMessage(connections.error)}
          onRetry={connections.retry}
        />
      ) : null}
      {connections.status === 'ready' && connections.data.length === 0 ? (
        <EmptyState
          inSection
          title="No account connected yet"
          description="Connect Hetzner to manage the DNS records of your domains here."
          action={connectButton}
        />
      ) : null}
      {connections.status === 'ready' && connections.data.length > 0 ? (
        <ul className="record-list" aria-label="Connected accounts">
          {connections.data.map((connection) => (
            <AccountRow
              key={connection.id}
              connection={connection}
              own={isOwnConnection(connection, user)}
              onCheck={check}
              onRemove={remove}
            />
          ))}
        </ul>
      ) : null}
      <ConnectDialog
        open={connecting || wantsConnect}
        onOpenChange={(open) => {
          setConnecting(open)
          if (!open) clearQuery()
        }}
        onSubmit={create}
      />
    </Section>
  )
}
