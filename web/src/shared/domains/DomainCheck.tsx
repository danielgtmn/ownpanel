import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { toast } from '@ui-sdk/react/toast'
import { errorMessage, getDomainStatus, pointAnyDomain } from '../api/index.ts'
import { useState } from 'react'
import { useResource } from '../hooks/index.ts'
import { FormDialog, StatusBadge } from '../ui/index.ts'
import { ServerAddresses } from './ServerAddresses.tsx'
import { pointedMessage, statusAction, statusText, statusTone } from './status.ts'

function ManualHelp({ domain }: { domain: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button onClick={() => setOpen(true)}>
        How to point it here<span className="visually-hidden"> for {domain}</span>
      </Button>
      <FormDialog
        open={open}
        onOpenChange={setOpen}
        title={`Point ${domain} here`}
        description={`${domain} isn't managed from here. Connect the company that manages it under DNS, or set an A or AAAA record for it to these addresses yourself.`}
      >
        <div className="form">
          <ServerAddresses />
          <div className="form-actions">
            <DialogClose>Close</DialogClose>
          </div>
        </div>
      </FormDialog>
    </>
  )
}

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
        {action === 'manual' ? <ManualHelp domain={domain} /> : null}
      </div>
    </div>
  )
}
