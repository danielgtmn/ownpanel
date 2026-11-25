import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { toast } from '@ui-sdk/react/toast'
import { useState, type FormEvent, type ReactNode } from 'react'
import { errorMessage, isSavedButNotApplied } from '../../shared/api/index.ts'
import {
  normaliseRights,
  RightsChoice,
  rightsText,
  validatePersonAddress,
  validateRights,
} from '../../shared/sharing/index.ts'
import { FormDialog, StatusBadge } from '../../shared/ui/index.ts'
import { removeMailboxGrant, setKeepSentCopy, setMailboxGrant } from './api.ts'
import { NOT_IN_EFFECT } from './rules.ts'
import type { Mailbox, MailboxAccessor } from './types.ts'

// A change that is saved but not yet passed on to the mail server is not a failure for the person.
async function saved(run: () => Promise<unknown>, success: string) {
  try {
    await run()
    toast.success(success)
  } catch (error) {
    if (!isSavedButNotApplied(error)) {
      toast.error(errorMessage(error))
      return
    }
    toast.warning(errorMessage(error))
  }
}

function FormError({ message }: { message?: string }) {
  return message ? (
    <p className="form-error" role="alert">
      {message}
    </p>
  ) : null
}

function personName(person: Pick<MailboxAccessor, 'address' | 'display_name'>): string {
  return person.display_name ? `${person.display_name} (${person.address})` : person.address
}

// ---- giving a person access to a shared mailbox ----

type GrantErrors = { address?: string; rights?: string; form?: string }

export function GrantForm({
  mailbox,
  existing,
  onSubmit,
}: {
  mailbox: Pick<Mailbox, 'address' | 'domain'>
  existing: MailboxAccessor | null
  onSubmit: (address: string, rights: string[]) => Promise<void>
}) {
  const [address, setAddress] = useState(existing?.address ?? '')
  const [rights, setRights] = useState<string[]>(existing ? normaliseRights(existing.rights) : ['read'])
  const [errors, setErrors] = useState<GrantErrors>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found: GrantErrors = {}
    const addressError = existing ? null : validatePersonAddress(address)
    if (addressError) found.address = addressError
    const rightsError = validateRights(rights)
    if (rightsError) found.rights = rightsError
    if (found.address || found.rights) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(address.trim().toLowerCase(), normaliseRights(rights))
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      {existing ? (
        <p className="mail-grant-person">{personName(existing)}</p>
      ) : (
        <Field name="address" invalid={errors.address ? true : undefined}>
          <FieldLabel>The person’s mailbox</FieldLabel>
          <Input
            value={address}
            type="email"
            placeholder={`anna@${mailbox.domain}`}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            autoFocus
            onValueChange={(value) => {
              setAddress(value)
              setErrors((current) => ({ ...current, address: undefined, form: undefined }))
            }}
          />
          <FieldDescription>
            A personal mailbox of the same organisation. The person uses their own sign-in.
          </FieldDescription>
          {errors.address ? <FieldError match>{errors.address}</FieldError> : null}
        </Field>
      )}
      <RightsChoice
        value={rights}
        error={errors.rights}
        disabled={busy}
        onChange={(next) => {
          setRights(next)
          setErrors((current) => ({ ...current, rights: undefined, form: undefined }))
        }}
      />
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : existing ? 'Save' : 'Give access'}
        </Button>
      </div>
    </form>
  )
}

function GrantDialog({
  mailbox,
  open,
  existing,
  onOpenChange,
  onSubmit,
}: {
  mailbox: Mailbox
  open: boolean
  existing: MailboxAccessor | null
  onOpenChange: (open: boolean) => void
  onSubmit: (address: string, rights: string[]) => Promise<void>
}) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={existing ? 'Change access' : 'Give a person access'}
      description={
        existing
          ? `What ${existing.address} may do with ${mailbox.address}.`
          : `${mailbox.address} shows up in the person’s mail program next to their own mailbox.`
      }
    >
      <GrantForm mailbox={mailbox} existing={existing} onSubmit={onSubmit} />
    </FormDialog>
  )
}

function AccessorItem({
  person,
  actions,
}: {
  person: MailboxAccessor
  actions: ReactNode
}) {
  return (
    <li className="access-item">
      <div className="access-item-main">
        <strong>{person.address}</strong>
        <ul className="record-facts">
          {person.display_name ? <li>{person.display_name}</li> : null}
          <li>{rightsText(person.rights)}</li>
        </ul>
        {person.active ? null : <p className="access-off">{NOT_IN_EFFECT}</p>}
      </div>
      <div className="record-actions">{actions}</div>
    </li>
  )
}

// ---- a shared mailbox: who has access, and its copy of sent mail ----

export function SharedAccessPanel({
  mailbox,
  onMailboxChanged,
}: {
  mailbox: Mailbox
  onMailboxChanged: () => Promise<void>
}) {
  const [dialog, setDialog] = useState<{ open: boolean; existing: MailboxAccessor | null }>({
    open: false,
    existing: null,
  })
  const [switching, setSwitching] = useState(false)

  async function grant(address: string, rights: string[]) {
    const existing = dialog.existing
    // Errors stay in the form; a change saved but not yet passed on is no failure.
    try {
      await setMailboxGrant(mailbox.id, address, rights)
      toast.success(
        existing ? `The access of ${address} was changed.` : `${address} now has access to ${mailbox.address}.`,
      )
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setDialog((current) => ({ ...current, open: false }))
    await onMailboxChanged()
  }

  async function remove(person: MailboxAccessor) {
    const yes = await confirm({
      title: `Take away the access of ${person.address}?`,
      description: `${person.address} can no longer read ${mailbox.address} or send from it. Its folders disappear from their mail program.`,
      confirmLabel: 'Take away access',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    await saved(() => removeMailboxGrant(mailbox.id, person.mailbox_id), `${person.address} no longer has access.`)
    await onMailboxChanged()
  }

  async function setKeep(next: boolean) {
    setSwitching(true)
    await saved(
      () => setKeepSentCopy(mailbox.id, next),
      next
        ? `Mail sent for ${mailbox.address} is now kept in its Sent folder.`
        : `Mail sent for ${mailbox.address} is no longer kept in its Sent folder.`,
    )
    await onMailboxChanged()
    setSwitching(false)
  }

  return (
    <div className="mail-apps">
      <p className="muted mail-apps-empty">
        People of the organisation use this mailbox with their own sign-in. It shows up in their mail program next to
        their own mailbox.
      </p>
      {mailbox.shared_with.length === 0 ? (
        <p className="muted access-empty">Nobody has access yet.</p>
      ) : (
        <ul className="access-list" aria-label={`People with access to ${mailbox.address}`}>
          {mailbox.shared_with.map((person) => (
            <AccessorItem
              key={person.mailbox_id}
              person={person}
              actions={
                <>
                  <Button onClick={() => setDialog({ open: true, existing: person })}>
                    Change<span className="visually-hidden"> the access of {person.address}</span>
                  </Button>
                  <Button data-variant="destructive" pendingAnnouncement="Removing" onClick={() => remove(person)}>
                    Remove<span className="visually-hidden"> {person.address}</span>
                  </Button>
                </>
              }
            />
          ))}
        </ul>
      )}
      <div className="record-actions">
        <Button onClick={() => setDialog({ open: true, existing: null })}>
          Give a person access<span className="visually-hidden"> to {mailbox.address}</span>
        </Button>
      </div>
      <Field className="mail-apps-switch">
        <Switch checked={mailbox.keep_sent_copy} disabled={switching} onCheckedChange={(next) => void setKeep(next === true)}>
          <SwitchThumb />
        </Switch>
        <div className="choice-text">
          <FieldLabel>Keep a copy of mail sent for this mailbox in its Sent folder</FieldLabel>
          <FieldDescription>So everyone with access sees what was sent from it, whoever sent it.</FieldDescription>
        </div>
      </Field>
      <GrantDialog
        mailbox={mailbox}
        open={dialog.open}
        existing={dialog.existing}
        onOpenChange={(open) => setDialog((current) => ({ ...current, open }))}
        onSubmit={grant}
      />
    </div>
  )
}

// ---- a personal mailbox: what it can use and who it is shared with ----

export function PersonalAccessPanel({
  mailbox,
  onMailboxChanged,
}: {
  mailbox: Mailbox
  onMailboxChanged: () => Promise<void>
}) {
  async function remove(person: MailboxAccessor) {
    const yes = await confirm({
      title: `Take away the access of ${person.address}?`,
      description: `${person.address} can no longer use ${mailbox.address}. The person who uses ${mailbox.address} can share it again in their own account settings.`,
      confirmLabel: 'Take away access',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    await saved(() => removeMailboxGrant(mailbox.id, person.mailbox_id), `${person.address} no longer has access.`)
    await onMailboxChanged()
  }

  return (
    <div className="mail-apps">
      <h4>Has access to</h4>
      {mailbox.access_to.length === 0 ? (
        <p className="muted access-empty">No other mailboxes.</p>
      ) : (
        <ul className="access-list" aria-label={`Mailboxes ${mailbox.address} has access to`}>
          {mailbox.access_to.map((access) => (
            <li className="access-item" key={access.mailbox_id}>
              <div className="access-item-main">
                <strong>
                  {access.address}
                  {access.kind === 'shared' ? <StatusBadge tone="neutral">Shared</StatusBadge> : null}
                </strong>
                <ul className="record-facts">
                  <li>{rightsText(access.rights)}</li>
                </ul>
                {access.active ? null : <p className="access-off">{NOT_IN_EFFECT}</p>}
              </div>
            </li>
          ))}
        </ul>
      )}
      <h4>Shared with</h4>
      {mailbox.shared_with.length === 0 ? (
        <p className="muted access-empty">Nobody.</p>
      ) : (
        <ul className="access-list" aria-label={`People ${mailbox.address} is shared with`}>
          {mailbox.shared_with.map((person) => (
            <AccessorItem
              key={person.mailbox_id}
              person={person}
              actions={
                <Button data-variant="destructive" pendingAnnouncement="Removing" onClick={() => remove(person)}>
                  Remove<span className="visually-hidden"> {person.address}</span>
                </Button>
              }
            />
          ))}
        </ul>
      )}
      <p className="muted access-empty">
        The person who uses this mailbox shares it in their own account settings. Here you can only take access away.
      </p>
    </div>
  )
}
