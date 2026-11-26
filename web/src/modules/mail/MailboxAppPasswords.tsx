import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage, isSavedButNotApplied } from '../../shared/api/index.ts'
import { formatDateTime } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { ErrorState, LoadingState } from '../../shared/ui/index.ts'
import { listAppPasswords, revokeAppPassword, updateMailbox } from './api.ts'
import { appPasswordUse } from './rules.ts'
import type { AppPassword, Mailbox } from './types.ts'

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

// MailboxAppPasswords lists a mailbox's app passwords and holds the switch
// that lets mail programs sign in with app passwords only. Creating one
// happens in a dialog of the page (onCreate); refreshKey reloads the list.
export function MailboxAppPasswords({
  mailbox,
  refreshKey,
  onCreate,
  onMailboxChanged,
}: {
  mailbox: Mailbox
  refreshKey: number
  onCreate: (mailbox: Mailbox) => void
  onMailboxChanged: () => Promise<void>
}) {
  const apps = useResource(() => listAppPasswords(mailbox.id), `app-passwords:${mailbox.id}:${refreshKey}`)
  const [switching, setSwitching] = useState(false)
  const list = apps.status === 'ready' ? apps.data : []

  async function revoke(app: AppPassword) {
    const last = mailbox.app_passwords_only && list.length === 1
    const yes = await confirm({
      title: `Revoke the app password “${app.name}”?`,
      description: `Mail programs using it will be signed out of ${mailbox.address} at once and can't sign in with it again.${
        last ? ' It is the last one: until you create a new one, no mail program can sign in to this mailbox.' : ''
      }`,
      confirmLabel: 'Revoke',
      cancelLabel: 'Keep it',
    })
    if (!yes) return
    await saved(() => revokeAppPassword(mailbox.id, app.id), `The app password “${app.name}” was revoked.`)
    await apps.reload()
  }

  async function setOnly(next: boolean) {
    if (next) {
      const none = apps.status === 'ready' && list.length === 0
      const yes = await confirm({
        title: `Allow only app passwords for ${mailbox.address}?`,
        description: none
          ? 'This mailbox has no app passwords yet. Every mail program and phone signed in with the mailbox password stops working until you create an app password and enter it there.'
          : 'Mail programs and phones signed in with the mailbox password stop working until you enter one of its app passwords there.',
        confirmLabel: 'Allow only app passwords',
        cancelLabel: 'Cancel',
      })
      if (!yes) return
    }
    setSwitching(true)
    await saved(
      () => updateMailbox(mailbox.id, { app_passwords_only: next }),
      next
        ? `Mail programs now sign in to ${mailbox.address} with app passwords only.`
        : `Mail programs can sign in to ${mailbox.address} with the mailbox password again.`,
    )
    await onMailboxChanged()
    setSwitching(false)
  }

  return (
    <div className="mail-apps">
      <Field className="mail-apps-switch">
        <Switch
          checked={mailbox.app_passwords_only}
          disabled={switching}
          onCheckedChange={(next) => void setOnly(next === true)}
        >
          <SwitchThumb />
        </Switch>
        <div className="choice-text">
          <FieldLabel>Only allow mail programs with app passwords</FieldLabel>
          <FieldDescription>
            The mailbox password then no longer works in mail programs and on phones; only app passwords do. A lost
            device is locked out by revoking its app password, without changing the others.
          </FieldDescription>
        </div>
      </Field>
      {apps.status === 'loading' ? <LoadingState label="Loading app passwords…" /> : null}
      {apps.status === 'error' ? (
        <ErrorState title="Couldn't load the app passwords" message={errorMessage(apps.error)} onRetry={apps.retry} />
      ) : null}
      {apps.status === 'ready' && list.length === 0 ? (
        <p className="muted mail-apps-empty">
          No app passwords yet. Create one for each phone or mail program, so you can sign out one device on its own.
        </p>
      ) : null}
      {list.length > 0 ? (
        <ul aria-label={`App passwords of ${mailbox.address}`}>
          {list.map((app) => (
            <li className="mail-app" key={app.id}>
              <div className="mail-app-main">
                <strong>{app.name}</strong>
                <ul className="record-facts">
                  <li>Created {formatDateTime(app.created_at)}</li>
                  <li>{appPasswordUse(app, formatDateTime)}</li>
                </ul>
              </div>
              <Button data-variant="destructive" pendingAnnouncement="Revoking" onClick={() => revoke(app)}>
                Revoke<span className="visually-hidden"> {app.name}</span>
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      <div className="record-actions">
        <Button onClick={() => onCreate(mailbox)}>
          Create app password<span className="visually-hidden"> for {mailbox.address}</span>
        </Button>
      </div>
    </div>
  )
}
