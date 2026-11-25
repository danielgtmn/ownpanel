import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { ApiError, errorMessage, isNotFound } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { hrefFor } from '../../shared/routing/index.ts'
import { copyText, ErrorState, FormDialog, LoadingState, Section, StatusBadge } from '../../shared/ui/index.ts'
import { applyMailRecords, getMailRecords } from './api.ts'
import {
  changeLabel,
  changeSentence,
  changesToConfirm,
  recordName,
  recordPurpose,
  recordStatus,
  recordsNeedWork,
} from './rules.ts'
import type { MailRecord, RecordChange } from './types.ts'

function CopyButton({ text, what }: { text: string; what: string }) {
  return (
    <Button className="copy-button" onClick={() => copyText(text)}>
      Copy<span className="visually-hidden"> {what}</span>
    </Button>
  )
}

function RecordRow({ record }: { record: MailRecord }) {
  const status = recordStatus(record.status)
  const name = recordName(record)
  return (
    <li className="mail-record">
      <div className="mail-record-head">
        <span className="mail-record-purpose">
          <strong>{record.type} record</strong>
          <span className="muted"> · {recordPurpose(record.kind)}</span>
        </span>
        <StatusBadge tone={status.tone}>{status.label}</StatusBadge>
      </div>
      <dl>
        <div>
          <dt>Name</dt>
          <dd>
            <code>{name}</code>
            {record.fqdn && record.fqdn !== name ? <span className="muted">({record.fqdn})</span> : null}
            <CopyButton text={name} what={`the name of the ${record.type} record`} />
          </dd>
        </div>
        <div>
          <dt>Value</dt>
          <dd>
            <code>{record.want}</code>
            <CopyButton text={record.want} what={`the value of the ${record.type} record`} />
          </dd>
        </div>
        {record.status !== 'ok' && record.found.length > 0 ? (
          <div>
            <dt>Now</dt>
            <dd>
              {record.found.map((value) => (
                <code key={value}>{value}</code>
              ))}
            </dd>
          </div>
        ) : null}
      </dl>
      {record.note ? <p className="muted mail-record-note">{record.note}</p> : null}
    </li>
  )
}

function ConfirmChanges({
  domain,
  changes,
  onConfirm,
}: {
  domain: string
  changes: RecordChange[]
  onConfirm: () => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const asked = changesToConfirm(changes)
  const rest = changes.filter((change) => !change.needs_confirm && change.action !== 'keep')

  async function confirm() {
    setBusy(true)
    setError(null)
    try {
      await onConfirm()
    } catch (failure) {
      setError(errorMessage(failure))
      setBusy(false)
    }
  }

  return (
    <div className="form">
      <ul className="mail-changes" aria-label="Changes that need your yes">
        {asked.map((change) => (
          <li key={`${change.kind}:${change.type}:${change.name}`}>
            <strong>
              {changeLabel(change.action)}: {change.type} record {change.name || '@'}
            </strong>
            <p>{changeSentence(change, domain)}</p>
            {change.note ? <p className="muted">{change.note}</p> : null}
            {change.old.length > 0 ? (
              <p className="muted">
                Now: <code>{change.old.join(', ')}</code>
              </p>
            ) : null}
            {change.new.length > 0 ? (
              <p className="muted">
                Afterwards: <code>{change.new.join(', ')}</code>
              </p>
            ) : null}
          </li>
        ))}
      </ul>
      {rest.length > 0 ? (
        <p className="muted">
          Also: {rest.map((change) => `${changeLabel(change.action).toLowerCase()} the ${change.type} record ${change.name || '@'}`).join('; ')}.
        </p>
      ) : null}
      {error ? (
        <p className="form-error" role="alert">
          {error}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button data-variant="solid" disabled={busy} onClick={confirm}>
          {busy ? 'Changing…' : 'Change the records'}
        </Button>
      </div>
    </div>
  )
}

export function RecordsSection({ domain, onApplied }: { domain: string; onApplied: () => void }) {
  const records = useResource(() => getMailRecords(domain), `mail-records:${domain}`)
  const [pending, setPending] = useState<RecordChange[] | null>(null)

  if (records.status === 'error' && isNotFound(records.error)) return null

  async function finish() {
    toast.success(`The mail records of ${domain} are set up.`)
    setPending(null)
    await records.reload()
    onApplied()
  }

  async function apply() {
    try {
      const result = await applyMailRecords(domain, false)
      if (result.applied) await finish()
      else if (changesToConfirm(result.changes).length > 0) setPending(result.changes)
      else {
        toast.info('Nothing needed to change.')
        await records.reload()
      }
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }

  async function confirmApply() {
    const result = await applyMailRecords(domain, true)
    if (!result.applied) throw new ApiError(0, "The records weren't changed. Check them below and try again.")
    await finish()
  }

  const data = records.status === 'ready' ? records.data : null
  const open = data ? recordsNeedWork(data.records) : 0

  return (
    <Section
      title="DNS records"
      description="These records send mail for the domain to this server and let other mail servers trust the mail it sends."
      actions={
        data?.can_apply && open > 0 ? (
          <Button data-variant="solid" pendingAnnouncement="Setting up" onClick={apply}>
            Set up records
          </Button>
        ) : null
      }
    >
      {records.status === 'loading' ? <LoadingState label="Checking the records…" /> : null}
      {records.status === 'error' ? (
        <ErrorState title="Couldn't check the records" message={errorMessage(records.error)} onRetry={records.retry} />
      ) : null}
      {data ? (
        <div className="mail-records">
          {open === 0 ? (
            <p className="notice">All records are set up.</p>
          ) : data.can_apply ? (
            <p>
              {data.zone} is managed through one of your DNS connections
              {data.connection_id !== null ? (
                <>
                  {' '}
                  (<a href={hrefFor('dns', String(data.connection_id), data.zone)}>show its records</a>)
                </>
              ) : null}
              . The panel can set up the {open === 1 ? 'missing record' : `${open} missing records`} for you.
            </p>
          ) : (
            <>
              <p>
                Create these records where you manage the DNS of {data.zone || domain}. Changes can take a while to
                show up everywhere.
              </p>
              {data.apply_note ? <p className="muted">{data.apply_note}</p> : null}
            </>
          )}
          <ul className="mail-record-list" aria-label={`Mail records of ${domain}`}>
            {data.records.map((record) => (
              <RecordRow key={`${record.kind}:${record.type}:${record.name}`} record={record} />
            ))}
          </ul>
        </div>
      ) : null}
      <FormDialog
        open={pending !== null}
        onOpenChange={(next) => {
          if (!next) setPending(null)
        }}
        title="Change existing records?"
        description={`Some records of ${domain} exist already and would change.`}
      >
        {pending ? <ConfirmChanges domain={domain} changes={pending} onConfirm={confirmApply} /> : null}
      </FormDialog>
    </Section>
  )
}
