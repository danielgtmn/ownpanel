import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { errorMessage, getDomainStatuses, pointDomain, type DomainStatus } from '../../shared/api/index.ts'
import {
  foundText,
  pointedMessage,
  PointHelp,
  statusAction,
  statusText,
  statusTone,
} from '../../shared/domains/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { StatusBadge } from '../../shared/ui/index.ts'

type RowProps = {
  siteName: string
  status: DomainStatus
  onPointed: () => Promise<void>
}

function DomainRow({ siteName, status, onPointed }: RowProps) {
  const action = statusAction(status)

  async function point() {
    try {
      const changed = await pointDomain(siteName, status.domain)
      toast.success(pointedMessage(status.domain, changed, status.zone?.zone))
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await onPointed()
  }

  return (
    <li className="domain-status-row">
      <div className="domain-status-main">
        <a className="inline-link domain-status-name" href={`https://${status.domain}`} target="_blank" rel="noreferrer noopener">
          {status.domain}
          <span className="visually-hidden">(opens in a new tab)</span>
        </a>
        <StatusBadge tone={statusTone(status)}>{statusText(status)}</StatusBadge>
        {foundText(status) ? <span className="muted domain-status-found">{foundText(status)}</span> : null}
      </div>
      {action === 'point' ? (
        <Button pendingAnnouncement="Pointing" onClick={point}>
          Point to this server<span className="visually-hidden"> for {status.domain}</span>
        </Button>
      ) : null}
      {action === 'manual' ? <PointHelp domains={[status.domain]} /> : null}
    </li>
  )
}

export function DomainsStatus({ siteName, domains }: { siteName: string; domains: string[] }) {
  const statuses = useResource(() => getDomainStatuses(siteName), `domain-status:${siteName}:${domains.join(',')}`)

  if (statuses.status === 'loading') {
    return (
      <>
        <ul className="chip-list" aria-label="Site domains">
          {domains.map((domain) => (
            <li key={domain}>{domain}</li>
          ))}
        </ul>
        <p className="muted domain-status-note">Checking where your domains point…</p>
      </>
    )
  }
  if (statuses.status === 'error') {
    return (
      <>
        <ul className="chip-list" aria-label="Site domains">
          {domains.map((domain) => (
            <li key={domain}>{domain}</li>
          ))}
        </ul>
        <p className="muted domain-status-note">
          Couldn't check where your domains point. {errorMessage(statuses.error)}{' '}
          <Button onClick={statuses.retry}>Try again</Button>
        </p>
      </>
    )
  }

  const canPoint = statuses.data.some((status) => statusAction(status) === 'point')
  return (
    <>
      <ul className="domain-status-list" aria-label="Site domains">
        {statuses.data.map((status) => (
          <DomainRow key={status.domain} siteName={siteName} status={status} onPointed={statuses.reload} />
        ))}
      </ul>
      {canPoint ? (
        <p className="muted domain-status-note">
          After you point a domain, DNS changes can take a few minutes to be visible.
        </p>
      ) : null}
    </>
  )
}
