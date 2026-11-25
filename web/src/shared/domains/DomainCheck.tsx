import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { errorMessage, getDomainStatus, pointAnyDomain } from '../api/index.ts'
import { useResource } from '../hooks/index.ts'
import { StatusBadge } from '../ui/index.ts'
import { PointHelp } from './PointHelp.tsx'
import { foundText, pointedMessage, statusAction, statusText, statusTone } from './status.ts'

export function DomainCheck({ domain }: { domain: string }) {
  const status = useResource(() => getDomainStatus(domain), `domain-check:${domain}`)

  if (status.status === 'loading') return <p className="muted domain-check">Checking where {domain} points…</p>
  if (status.status === 'error') {
    return (
      <p className="muted domain-check">
        Couldn't check where {domain} points. {errorMessage(status.error)}{' '}
        <Button onClick={status.retry}>
          Try again<span className="visually-hidden"> for {domain}</span>
        </Button>
      </p>
    )
  }

  const current = status.data
  const action = statusAction(current)

  async function point() {
    try {
      const changed = await pointAnyDomain(domain)
      toast.success(pointedMessage(domain, changed, current.zone?.zone))
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await status.reload()
  }

  return (
    <div className="domain-check">
      <div className="domain-check-row">
        <StatusBadge tone={statusTone(current)}>{statusText(current)}</StatusBadge>
        {action === 'point' ? (
          <Button pendingAnnouncement="Pointing" onClick={point}>
            Point to this server<span className="visually-hidden"> for {domain}</span>
          </Button>
        ) : null}
        {action === 'manual' ? <PointHelp domains={[domain]} /> : null}
      </div>
      {foundText(current) ? <p className="muted domain-check-found">{foundText(current)}</p> : null}
    </div>
  )
}
