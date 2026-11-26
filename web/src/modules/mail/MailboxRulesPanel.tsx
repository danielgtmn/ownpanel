import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState, type FormEvent } from 'react'
import { ApiError, errorMessage } from '../../shared/api/index.ts'
import { formatDateTime } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import {
  formatDay,
  forwardingBody,
  forwardingDraft,
  forwardingError,
  ForwardingFields,
  hasErrors,
  outOfOfficeBody,
  outOfOfficeDraft,
  outOfOfficeErrors,
  OutOfOfficeFields,
  SwitchChoice,
  type ForwardingDraft,
  type OutOfOfficeDraft,
  type OutOfOfficeErrors,
} from '../../shared/mailrules/index.ts'
import { ErrorState, LoadingState } from '../../shared/ui/index.ts'
import { getMailboxRules, setMailboxForwarding, setMailboxMayForward, setMailboxOutOfOffice } from './api.ts'
import { forwardingStatus, outOfOfficeStatus } from './rules.ts'
import type { Mailbox, MailboxRules } from './types.ts'

function FormError({ message }: { message?: string | null }) {
  return message ? (
    <p className="form-error" role="alert">
      {message}
    </p>
  ) : null
}

// Sends a change; filters the person wrote in a mail program are only replaced after asking. Resolves with the
// new state, or null when the person cancelled.
async function withReplace(
  mailbox: Mailbox,
  rules: MailboxRules,
  send: (replace: boolean) => Promise<MailboxRules>,
): Promise<MailboxRules | null> {
  const ask = () =>
    confirm({
      title: `Replace the filters of ${mailbox.address}?`,
      description:
        "Its filters were written in a mail program. Saving here replaces them: they stop sorting, answering or forwarding the person's mail, and only what is set here is used.",
      confirmLabel: 'Replace them',
      cancelLabel: 'Cancel',
    })
  if (rules.hand_written) return (await ask()) ? send(true) : null
  try {
    return await send(false)
  } catch (error) {
    // Written in a mail program since this was loaded.
    if (error instanceof ApiError && error.status === 409 && error.code === 'hand_written_filters') {
      return (await ask()) ? send(true) : null
    }
    throw error
  }
}

function OutOfOfficeForm({
  mailbox,
  rules,
  onSaved,
}: {
  mailbox: Mailbox
  rules: MailboxRules
  onSaved: (next: MailboxRules) => void
}) {
  const reply = rules.out_of_office
  const [draft, setDraft] = useState<OutOfOfficeDraft>(() => outOfOfficeDraft(reply))
  const [errors, setErrors] = useState<OutOfOfficeErrors>({})
  const [form, setForm] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = outOfOfficeErrors(draft)
    if (hasErrors(found)) {
      setErrors(found)
      return
    }
    setBusy(true)
    setForm(null)
    try {
      const next = await withReplace(mailbox, rules, (replace) =>
        setMailboxOutOfOffice(mailbox.id, outOfOfficeBody(draft), replace),
      )
      if (next) {
        setDraft(outOfOfficeDraft(next.out_of_office))
        onSaved(next)
        toast.success(
          next.out_of_office.enabled
            ? `${mailbox.address} now answers with an out-of-office reply.`
            : `${mailbox.address} no longer answers with an out-of-office reply.`,
        )
      }
    } catch (error) {
      setForm(errorMessage(error))
    }
    setBusy(false)
  }

  return (
    <form className="form mail-rules-part" onSubmit={submit} noValidate>
      <h5>Out-of-office reply</h5>
      <p className="mailrules-set-by">{outOfOfficeStatus(reply, formatDay, formatDateTime)}</p>
      {reply.enabled && !reply.set_by ? (
        <p className="muted mail-rules-note">Saving here replaces the reply the person wrote.</p>
      ) : null}
      <fieldset className="mailrules-fieldset" disabled={busy}>
        <OutOfOfficeFields
          draft={draft}
          errors={errors}
          switchLabel="Answer mail with an out-of-office reply"
          onChange={(change) => {
            setDraft((current) => ({ ...current, ...change }))
            setErrors({})
            setForm(null)
          }}
        />
      </fieldset>
      <FormError message={form} />
      <div className="record-actions">
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Save reply'}
          <span className="visually-hidden"> of {mailbox.address}</span>
        </Button>
      </div>
    </form>
  )
}

function ForwardingForm({
  mailbox,
  rules,
  onSaved,
}: {
  mailbox: Mailbox
  rules: MailboxRules
  onSaved: (next: MailboxRules) => void
}) {
  const forwarding = rules.forwarding
  const [draft, setDraft] = useState<ForwardingDraft>(() => forwardingDraft(forwarding))
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = forwardingError(draft)
    if (found) {
      setError(found)
      return
    }
    setBusy(true)
    setForm(null)
    try {
      const next = await withReplace(mailbox, rules, (replace) =>
        setMailboxForwarding(mailbox.id, forwardingBody(draft), replace),
      )
      if (next) {
        setDraft(forwardingDraft(next.forwarding))
        onSaved(next)
        toast.success(
          next.forwarding.enabled
            ? `Mail to ${mailbox.address} is now forwarded to ${next.forwarding.to}.`
            : `Mail to ${mailbox.address} is no longer forwarded.`,
        )
      }
    } catch (failure) {
      setForm(errorMessage(failure))
    }
    setBusy(false)
  }

  return (
    <form className="form mail-rules-part" onSubmit={submit} noValidate>
      <h5>Forwarding</h5>
      <p className="mailrules-set-by">{forwardingStatus(forwarding, formatDateTime)}</p>
      {forwarding.enabled && !forwarding.set_by ? (
        <p className="muted mail-rules-note">Saving here replaces the forwarding the person set up.</p>
      ) : null}
      {!rules.may_forward ? (
        <p className="notice mail-rules-note">
          Forwarding is turned off for this mailbox. Allow it below before forwarding its mail.
        </p>
      ) : null}
      <fieldset className="mailrules-fieldset" disabled={busy}>
        <ForwardingFields
          draft={draft}
          error={error}
          switchLabel="Forward its mail"
          onChange={(change) => {
            setDraft((current) => ({ ...current, ...change }))
            setError(null)
            setForm(null)
          }}
        />
      </fieldset>
      <FormError message={form} />
      <div className="record-actions">
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Save forwarding'}
          <span className="visually-hidden"> of {mailbox.address}</span>
        </Button>
      </div>
    </form>
  )
}

export function MailboxRulesForms({
  mailbox,
  rules,
  onRules,
}: {
  mailbox: Mailbox
  rules: MailboxRules
  onRules: (next: MailboxRules) => void
}) {
  const [switching, setSwitching] = useState(false)

  async function setMayForward(allowed: boolean) {
    if (!allowed) {
      const yes = await confirm({
        title: `Stop ${mailbox.address} from forwarding mail?`,
        description:
          "Neither the person's own filters nor forwarding set here send its mail on to other addresses any more. The mail stays in the mailbox.",
        confirmLabel: 'Turn off forwarding',
        cancelLabel: 'Cancel',
      })
      if (!yes) return
    }
    setSwitching(true)
    try {
      await setMailboxMayForward(mailbox.id, allowed)
      onRules({ ...rules, may_forward: allowed })
      toast.success(
        allowed ? `${mailbox.address} may forward mail again.` : `${mailbox.address} no longer forwards mail.`,
      )
    } catch (error) {
      toast.error(errorMessage(error))
    }
    setSwitching(false)
  }

  return (
    <div className="mail-rules">
      <p className="muted mail-rules-note">
        For example for someone who left: answer their mail and send it on to a colleague. The person sees in their
        mailbox's settings who set it, and the activity log records it.
      </p>
      {rules.hand_written ? (
        <p className="notice mail-rules-note">
          The person's filters were written in a mail program. Saving here replaces them; you're asked first.
        </p>
      ) : null}
      <OutOfOfficeForm mailbox={mailbox} rules={rules} onSaved={onRules} />
      <ForwardingForm mailbox={mailbox} rules={rules} onSaved={onRules} />
      <div className="mail-rules-part">
        <SwitchChoice
          label="Filters of this mailbox may forward mail"
          description="When off, nothing sends its mail on to other addresses, not even filters the person set up."
          checked={rules.may_forward}
          disabled={switching}
          onCheckedChange={(next) => void setMayForward(next)}
        />
      </div>
    </div>
  )
}

// The out-of-office reply and forwarding of a mailbox, for the domain owner (opened below its actions).
export function MailboxRulesPanel({ mailbox, onChanged }: { mailbox: Mailbox; onChanged: () => void }) {
  const resource = useResource(() => getMailboxRules(mailbox.id), `mailbox-rules:${mailbox.id}`)
  const [latest, setLatest] = useState<MailboxRules | null>(null)
  const rules = latest ?? (resource.status === 'ready' ? resource.data : null)

  if (!rules) {
    if (resource.status === 'error') {
      return (
        <ErrorState
          title="Couldn't load the out-of-office reply and forwarding"
          message={errorMessage(resource.error)}
          onRetry={resource.retry}
        />
      )
    }
    return <LoadingState label="Loading…" />
  }

  return (
    <MailboxRulesForms
      mailbox={mailbox}
      rules={rules}
      onRules={(next) => {
        setLatest(next)
        onChanged()
      }}
    />
  )
}
