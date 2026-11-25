import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Meter, MeterIndicator, MeterTrack } from '@ui-sdk/react/meter'
import { toast } from '@ui-sdk/react/toast'
import { useEffect, useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDateTime } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { FormDialog, PasswordField, StatusBadge } from '../../shared/ui/index.ts'
import { cancelImport, continueImport, getImport, listImports } from './api.ts'
import { isActive, isFinished, jobCounts, jobShare, jobSource, jobState, needsPassword } from './transfer.ts'
import type { MailImport, Mailbox } from './types.ts'

type Target = Pick<Mailbox, 'domain' | 'local_part' | 'address'>

// How often the list is asked again while an import runs.
const POLL_MS = 3000

function ContinueForm({ job, onSubmit }: { job: MailImport; onSubmit: (password: string) => Promise<void> }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!password) {
      setError('Enter the password of the other account.')
      return
    }
    setBusy(true)
    try {
      await onSubmit(password)
    } catch (failure) {
      setError(errorMessage(failure))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <PasswordField
        name="password"
        label={`Password of ${job.username ?? 'the other account'}`}
        value={password}
        autoComplete="current-password"
        autoFocus
        description="It isn't saved; that's why it is needed again."
        error={error}
        onValueChange={(value) => {
          setPassword(value)
          setError(null)
        }}
      />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Continuing…' : 'Continue'}
        </Button>
      </div>
    </form>
  )
}

function Failures({ target, job }: { target: Target; job: MailImport }) {
  const detail = useResource(() => getImport(target.domain, target.local_part, job.id), `import:${target.address}:${job.id}`)
  if (detail.status === 'loading') return <p className="muted">Loading…</p>
  if (detail.status === 'error') return <p className="form-error">{errorMessage(detail.error)}</p>
  const failures = detail.data.failures ?? []
  if (failures.length === 0) return <p className="muted">No messages were left out.</p>
  return (
    <ul className="import-failures">
      {failures.map((failure, index) => (
        <li key={index}>
          <span>{failure.subject || '(no subject)'}</span>
          <span className="muted">
            {failure.folder}
            {failure.date ? ` · ${formatDateTime(failure.date)}` : ''} · {failure.reason}
          </span>
        </li>
      ))}
    </ul>
  )
}

function ImportRow({
  target,
  job,
  onCancel,
  onContinue,
  onChoose,
}: {
  target: Target
  job: MailImport
  onCancel: (job: MailImport) => Promise<void>
  onContinue: (job: MailImport) => void
  onChoose: (job: MailImport) => void
}) {
  const [showFailures, setShowFailures] = useState(false)
  const state = jobState(job)
  const share = jobShare(job)
  return (
    <li className="import-job">
      <div className="import-job-head">
        <span className="import-job-source">From {jobSource(job)}</span>
        <StatusBadge tone={state.tone}>{state.label}</StatusBadge>
      </div>
      {share !== null && !isFinished(job) ? (
        <Meter value={share} aria-label={`Progress of the import from ${jobSource(job)}`}>
          <MeterTrack>
            <MeterIndicator />
          </MeterTrack>
        </Meter>
      ) : null}
      <p className="muted import-job-counts">
        {job.state === 'uploading' ? state.sentence : jobCounts(job)}
        {job.finished ? ` · ended ${formatDateTime(job.finished)}` : ` · started ${formatDateTime(job.started || job.created)}`}
      </p>
      {job.error ? <p className="notice import-job-error">{job.error}</p> : null}
      {!job.error && (job.state === 'paused' || job.state === 'interrupted' || job.state === 'waiting') ? (
        <p className="muted import-job-error">{state.sentence}</p>
      ) : null}
      <div className="record-actions">
        {needsPassword(job) ? <Button onClick={() => onContinue(job)}>Continue</Button> : null}
        {job.state === 'ready' ? <Button onClick={() => onChoose(job)}>Choose folders and start</Button> : null}
        {job.failed > 0 ? (
          <Button onClick={() => setShowFailures((open) => !open)} aria-expanded={showFailures}>
            {showFailures ? 'Hide messages not copied' : 'Show messages not copied'}
          </Button>
        ) : null}
        <Button
          data-variant={isFinished(job) ? undefined : 'destructive'}
          pendingAnnouncement={isFinished(job) ? 'Removing' : 'Cancelling'}
          onClick={() => onCancel(job)}
        >
          {isFinished(job) ? 'Remove from list' : 'Cancel import'}
        </Button>
      </div>
      {showFailures ? <Failures target={target} job={job} /> : null}
    </li>
  )
}

// MailboxImports lists a mailbox's imports and asks again while one runs.
export function MailboxImports({
  target,
  refreshKey,
  onChoose,
  onSettled,
}: {
  target: Target
  refreshKey: number
  onChoose: (job: MailImport) => void
  // Called when the last running import ended (the mailbox holds more now).
  onSettled: () => void
}) {
  const imports = useResource(() => listImports(target.domain, target.local_part), `imports:${target.address}:${refreshKey}`)
  const [continuing, setContinuing] = useState<MailImport | null>(null)
  const list = imports.status === 'ready' ? imports.data : []
  const active = list.some(isActive)
  const reload = imports.reload

  useEffect(() => {
    if (!active) return
    const timer = setInterval(() => void reload(), POLL_MS)
    return () => clearInterval(timer)
  }, [active, reload])

  const [wasActive, setWasActive] = useState(active)
  if (active !== wasActive) {
    setWasActive(active)
    if (!active) onSettled()
  }

  async function cancel(job: MailImport) {
    if (!isFinished(job)) {
      const yes = await confirm({
        title: 'Cancel this import?',
        description: 'Copying stops. Mail copied until now stays in the mailbox.',
        confirmLabel: 'Cancel import',
        cancelLabel: 'Keep going',
      })
      if (!yes) return
    }
    try {
      await cancelImport(target.domain, target.local_part, job.id)
      if (!isFinished(job)) toast.success('The import was cancelled.')
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await reload()
  }

  async function resume(password: string) {
    if (!continuing) return
    await continueImport(target.domain, target.local_part, continuing.id, password)
    toast.success('The import goes on where it stopped.')
    setContinuing(null)
    await reload()
  }

  if (list.length === 0) return null
  return (
    <div className="import-jobs">
      <h4>Imports</h4>
      <ul aria-label={`Imports into ${target.address}`}>
        {list.map((job) => (
          <ImportRow key={job.id} target={target} job={job} onCancel={cancel} onContinue={setContinuing} onChoose={onChoose} />
        ))}
      </ul>
      <FormDialog
        open={continuing !== null}
        onOpenChange={(open) => {
          if (!open) setContinuing(null)
        }}
        title="Continue the import"
        description={
          continuing ? `Copying from ${jobSource(continuing)} goes on where it stopped. Mail that arrived meanwhile is copied too.` : ''
        }
      >
        {continuing ? <ContinueForm job={continuing} onSubmit={resume} /> : <span />}
      </FormDialog>
    </div>
  )
}
