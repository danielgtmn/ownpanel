import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import {
  Dialog,
  DialogBackdrop,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from '@ui-sdk/react/dialog'
import { Field, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { toast } from '@ui-sdk/react/toast'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDateTime, isoTimestamp } from '../../shared/format/index.ts'
import { copyText } from '../../shared/ui/index.ts'
import {
  acceptGitHostKey,
  checkGitSource,
  gitHostSteps,
  replaceDeployKey,
  replaceNotifySecret,
  type SiteSource,
} from './source.ts'

/** A notification secret just made, with where it belongs. */
export type ShownSecret = { secret: string; url: string; server?: string }

type Props = {
  site: string
  source: SiteSource
  onChange: () => Promise<void>
  onSecret: (secret: ShownSecret) => void
}

/**
 * The parts of a source linked by address that the person sets up at their
 * Git host: the deploy key, the server key the panel trusts, and where
 * pushes are reported. Nothing here is secret; the notification secret is
 * only shown once, in NotifySecretDialog.
 */
export function GitSourceDetails({ site, source, onChange, onSecret }: Props) {
  const steps = gitHostSteps(source.server)

  async function check() {
    try {
      const result = await checkGitSource(site)
      if (!result.ok) {
        toast.error(result.source?.problem ?? "Couldn't reach the repository.")
      } else if (!result.branch_found) {
        toast.error(`The repository has no branch “${source.branch}”. Choose another one with Change.`)
      } else {
        toast.success(`The panel can read the repository, and its branch ${source.branch} is there.`)
      }
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await onChange()
  }

  async function newKey() {
    const yes = await confirm({
      title: `Make a new deploy key for ${site}?`,
      description:
        'The current key stops working for this site at once. Add the new key to the repository, and remove the old one there.',
      confirmLabel: 'Make a new key',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await replaceDeployKey(site)
      toast.success('New deploy key made. Add it to the repository.')
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await onChange()
  }

  async function newSecret() {
    const yes = await confirm({
      title: 'Make a new notification address?',
      description:
        'The current address and secret stop working at once. Enter the new ones at your Git host, or pushes are no longer reported.',
      confirmLabel: 'Make a new one',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      const state = await replaceNotifySecret(site)
      if (state.notify_secret) {
        onSecret({ secret: state.notify_secret, url: state.source?.notify_url ?? '', server: state.source?.server })
      }
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await onChange()
  }

  async function accept(fingerprint: string) {
    const yes = await confirm({
      title: `Trust the new server key of ${source.server}?`,
      description: `Its fingerprint is ${fingerprint}. Accept it only if your Git host announced the change and publishes this fingerprint; otherwise someone may be pretending to be the server.`,
      confirmLabel: 'Trust the new key',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await acceptGitHostKey(site, fingerprint)
      toast.success(`The panel trusts the new server key of ${source.server}.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await onChange()
  }

  return (
    <div className="git-source">
      {source.deploy_key ? (
        <div className="git-source-part">
          <h4 className="git-source-title">Deploy key</h4>
          <p className="muted">{steps.key}</p>
          <code className="git-source-value">{source.deploy_key}</code>
          <div className="record-actions">
            <Button onClick={() => copyText(source.deploy_key ?? '')}>
              Copy<span className="visually-hidden"> the deploy key</span>
            </Button>
            <Button onClick={newKey}>Make a new key</Button>
          </div>
        </div>
      ) : null}
      {source.host_key ? (
        <div className="git-source-part">
          <h4 className="git-source-title">Server key</h4>
          <p className="muted">The panel only fetches from {source.server} while it shows this key:</p>
          <code className="git-source-value">{source.host_key}</code>
          {source.new_host_key ? (
            <>
              <p className="source-problem">It now shows this one instead:</p>
              <code className="git-source-value">{source.new_host_key}</code>
              <div className="record-actions">
                <Button onClick={() => accept(source.new_host_key ?? '')}>Trust the new key</Button>
              </div>
            </>
          ) : null}
        </div>
      ) : null}
      <div className="git-source-part">
        <h4 className="git-source-title">Report pushes</h4>
        {source.notify_url ? (
          <>
            <p className="muted">{steps.notify} The secret was shown once when the address was made.</p>
            <code className="git-source-value">{source.notify_url}</code>
            <div className="record-actions">
              <Button onClick={() => copyText(source.notify_url ?? '')}>
                Copy<span className="visually-hidden"> the notification address</span>
              </Button>
              <Button onClick={newSecret}>New address and secret</Button>
            </div>
          </>
        ) : (
          <p className="muted">{source.notify_problem}</p>
        )}
      </div>
      <div className="record-actions">
        <Button pendingAnnouncement="Checking access" onClick={check}>
          Check access
        </Button>
        {source.checked_at ? (
          <span className="muted git-source-checked">
            Last checked <time dateTime={isoTimestamp(source.checked_at)}>{formatDateTime(source.checked_at)}</time>
          </span>
        ) : null}
      </div>
    </div>
  )
}

/** Shows a new notification secret once. */
export function NotifySecretDialog({ shown, onClose }: { shown: ShownSecret | null; onClose: () => void }) {
  const steps = gitHostSteps(shown?.server)
  return (
    <Dialog
      open={shown !== null}
      onOpenChange={(next, details) => {
        if (next) return
        if (details.reason === 'outside-press') {
          details.cancel()
          return
        }
        onClose()
      }}
    >
      <DialogBackdrop />
      <DialogContent>
        <div className="dialog-head">
          <DialogTitle>Report pushes to the panel</DialogTitle>
          <DialogDescription>
            {steps.notify} Copy the secret now: it will not be shown again. If you lose it, make a new address.
          </DialogDescription>
        </div>
        <div className="form">
          {shown?.url ? (
            <Field name="notify_url">
              <FieldLabel>Address</FieldLabel>
              <Input readOnly value={shown.url} spellCheck={false} onFocus={(event) => event.currentTarget.select()} />
            </Field>
          ) : null}
          <Field name="notify_secret">
            <FieldLabel>Secret</FieldLabel>
            <Input
              readOnly
              value={shown?.secret ?? ''}
              spellCheck={false}
              autoComplete="off"
              className="git-secret"
              onFocus={(event) => event.currentTarget.select()}
            />
          </Field>
          <div className="form-actions">
            {shown?.url ? <Button onClick={() => copyText(shown.url)}>Copy address</Button> : null}
            <Button onClick={() => (shown ? copyText(shown.secret) : undefined)}>Copy secret</Button>
            <DialogClose data-variant="solid">I’ve entered it</DialogClose>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
