import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Meter, MeterIndicator, MeterTrack } from '@ui-sdk/react/meter'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { formatDateTime } from '../shared/format/index.ts'
import { rightsText, withGrant, withoutGrant } from '../shared/sharing/index.ts'
import { copyText, Section, StatusBadge } from '../shared/ui/index.ts'
import {
  ChangePasswordDialog,
  CreateAppPasswordDialog,
  DisplayNameDialog,
  NewAppPasswordDialog,
  ShareDialog,
  type CreatedAppPassword,
  type PasswordChange,
} from './AccountDialogs.tsx'
import {
  accountErrorMessage,
  APPLE_SETUP_URL,
  changePassword,
  createAppPassword,
  isSessionEnded,
  loadMe,
  revokeAppPassword,
  setAppPasswordsOnly,
  setDelegations,
  setDisplayName,
  signOut,
  type AppPassword,
  type Delegate,
  type Me,
} from './api.ts'
import {
  accessKindText,
  appPasswordUse,
  clientRows,
  folderText,
  messagesText,
  passwordChangedNotice,
  usageShare,
  usageText,
} from './rules.ts'

type Dialog = 'name' | 'password' | 'app' | null

// Shows a failed request as a toast, unless the session ended (the sign-in screen takes over then).
function failed(error: unknown) {
  if (!isSessionEnded(error)) toast.error(accountErrorMessage(error))
}

export function AccountPage({
  host,
  me,
  onChange,
  onSignedOut,
}: {
  host: string
  me: Me
  onChange: (me: Me) => void
  onSignedOut: () => void
}) {
  const [dialog, setDialog] = useState<Dialog>(null)
  const [created, setCreated] = useState<CreatedAppPassword | null>(null)
  const [switching, setSwitching] = useState(false)
  const [signingOut, setSigningOut] = useState(false)
  const [sharing, setSharing] = useState<{ open: boolean; existing: Delegate | null }>({ open: false, existing: null })
  const share = usageShare(me)
  const close = (open: boolean) => {
    if (!open) setDialog(null)
  }

  async function saveName(name: string) {
    onChange(await setDisplayName(name))
    setDialog(null)
    toast.success(name ? `Your name is now “${name}”.` : 'Your mail is now sent without a name.')
  }

  async function savePassword(change: PasswordChange) {
    const { warning, ...next } = await changePassword(change.current, change.password, change.revokeAppPasswords)
    onChange(next)
    setDialog(null)
    const notice = passwordChangedNotice(change.revokeAppPasswords, warning)
    if (notice.kind === 'warning') toast.warning(notice.text)
    else toast.success(notice.text)
  }

  async function addAppPassword(name: string) {
    const result = await createAppPassword(name)
    setDialog(null)
    setCreated({ address: me.address, name: result.app_password.name, password: result.password })
    try {
      onChange(await loadMe())
    } catch (error) {
      failed(error)
    }
  }

  async function revoke(app: AppPassword) {
    const last = me.app_passwords_only && me.app_passwords.length === 1
    const yes = await confirm({
      title: `Revoke the app password “${app.name}”?`,
      description: `Mail programs using it are signed out at once and can't sign in with it again.${
        last ? ' It is your last one: until you create a new one, no mail program can sign in to your mailbox.' : ''
      }`,
      confirmLabel: 'Revoke',
      cancelLabel: 'Keep it',
    })
    if (!yes) return
    try {
      await revokeAppPassword(app.id)
      toast.success(`The app password “${app.name}” was revoked.`)
      onChange(await loadMe())
    } catch (error) {
      failed(error)
    }
  }

  async function setOnly(next: boolean) {
    if (next) {
      const yes = await confirm({
        title: 'Allow only app passwords in mail programs?',
        description:
          me.app_passwords.length === 0
            ? "You have no app passwords yet. Every mail program and phone signed in with your mailbox password stops working until you create an app password and enter it there. This page keeps working with your mailbox password."
            : 'Mail programs and phones signed in with your mailbox password stop working until you enter one of your app passwords there. This page keeps working with your mailbox password.',
        confirmLabel: 'Allow only app passwords',
        cancelLabel: 'Cancel',
      })
      if (!yes) return
    }
    setSwitching(true)
    try {
      onChange(await setAppPasswordsOnly(next))
      toast.success(
        next
          ? 'Mail programs now sign in with app passwords only.'
          : 'Mail programs can sign in with your mailbox password again.',
      )
    } catch (error) {
      failed(error)
    }
    setSwitching(false)
  }

  // The server takes the whole list each time.
  async function saveShare(address: string, rights: string[]) {
    const changing = sharing.existing !== null
    onChange(await setDelegations(withGrant(me.shared_with, address, rights)))
    setSharing((current) => ({ ...current, open: false }))
    toast.success(changing ? `What ${address} may do was changed.` : `Your mailbox is now shared with ${address}.`)
  }

  async function unshare(person: Delegate) {
    const yes = await confirm({
      title: `Stop sharing with ${person.address}?`,
      description: `${person.address} can no longer use your mailbox. It disappears from their mail program.`,
      confirmLabel: 'Stop sharing',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      onChange(await setDelegations(withoutGrant(me.shared_with, person.address)))
      toast.success(`Your mailbox is no longer shared with ${person.address}.`)
    } catch (error) {
      failed(error)
    }
  }

  async function leave() {
    setSigningOut(true)
    try {
      await signOut()
    } catch {
      // Signed out on this device either way: the cookie is gone or the session ended.
    }
    onSignedOut()
  }

  return (
    <div className="account">
      <header className="account-top">
        <span className="brand">{host}</span>
        <Button onClick={leave} disabled={signingOut}>
          {signingOut ? 'Signing out…' : 'Sign out'}
        </Button>
      </header>
      <main className="account-main">
        <header className="page-header">
          <div className="page-header-row">
            <div className="page-header-text">
              <h1 className="account-address">{me.address}</h1>
              <p className="muted">
                {me.display_name ? `Mail you send shows the name ${me.display_name}.` : 'Mail you send shows no name.'}
              </p>
            </div>
          </div>
        </header>

        <Section title="Storage" description="How much room your mail takes on the server.">
          <div className="account-usage">
            {share !== null ? (
              <Meter value={Math.round(share * 100)} aria-label="Storage used">
                <MeterTrack>
                  <MeterIndicator />
                </MeterTrack>
              </Meter>
            ) : null}
            <span>{usageText(me)}</span>
            <span className="muted">{messagesText(me.messages)}</span>
          </div>
          {share !== null && share >= 0.9 ? (
            <p className="notice account-notice">
              Your mailbox is almost full. Delete mail you no longer need, or new mail can't be stored.
            </p>
          ) : null}
        </Section>

        <Section
          title="Your name"
          description="The name people see next to your address."
          actions={<Button onClick={() => setDialog('name')}>Change name</Button>}
        >
          <p className="account-value">{me.display_name || <span className="muted">No name set</span>}</p>
        </Section>

        <Section
          title="Password"
          description="The password you sign in with here and, unless you allow only app passwords, in mail programs. Choose one you don't use anywhere else."
        >
          <div className="record-actions">
            <Button onClick={() => setDialog('password')}>Change password</Button>
          </div>
        </Section>

        <Section
          title="App passwords"
          description="A password of its own for each phone or mail program. A lost device is locked out by revoking its app password, without changing the others or your mailbox password."
        >
          <div className="account-apps">
            {me.app_passwords_available ? null : (
              <p className="notice">App passwords can't be created on this server yet. Ask the people who manage it.</p>
            )}
            <Field className="account-choice">
              <Switch
                checked={me.app_passwords_only}
                disabled={switching}
                onCheckedChange={(next) => void setOnly(next === true)}
              >
                <SwitchThumb />
              </Switch>
              <div className="choice-text">
                <FieldLabel>Only allow mail programs with app passwords</FieldLabel>
                <FieldDescription>
                  Your mailbox password then no longer works in mail programs and on phones, only here. Someone who
                  learns it can't read your mail in a mail program.
                </FieldDescription>
              </div>
            </Field>
            {me.app_passwords.length === 0 ? (
              <p className="muted account-empty">
                No app passwords yet. Create one for each phone or mail program, so you can sign out one device on its
                own.
              </p>
            ) : (
              <ul aria-label="Your app passwords">
                {me.app_passwords.map((app) => (
                  <li className="account-app" key={app.id}>
                    <div className="account-app-main">
                      <strong>{app.name}</strong>
                      <ul className="record-facts">
                        <li>Created {formatDateTime(app.created_at)}</li>
                        <li>{appPasswordUse(app, me.uses_known_days, formatDateTime)}</li>
                      </ul>
                    </div>
                    <Button data-variant="destructive" pendingAnnouncement="Revoking" onClick={() => revoke(app)}>
                      Revoke<span className="visually-hidden"> {app.name}</span>
                    </Button>
                  </li>
                ))}
              </ul>
            )}
            {me.app_passwords_available ? (
              <div className="record-actions">
                <Button onClick={() => setDialog('app')}>Create app password</Button>
              </div>
            ) : null}
          </div>
        </Section>

        <Section
          title="Your mailbox is shared with"
          description="People of your organisation who may use your mailbox with their own sign-in, for example an assistant. It shows up in their mail program next to their own."
          actions={
            me.shared_with.length > 0 ? (
              <Button onClick={() => setSharing({ open: true, existing: null })}>Share with someone</Button>
            ) : null
          }
        >
          {me.shared_with.length === 0 ? (
            <div className="account-sharing-empty">
              <p className="muted account-empty">Nobody. Only you can use your mailbox.</p>
              <div className="record-actions">
                <Button onClick={() => setSharing({ open: true, existing: null })}>Share with someone</Button>
              </div>
            </div>
          ) : (
            <ul className="access-list" aria-label="People your mailbox is shared with">
              {me.shared_with.map((person) => (
                <li className="access-item" key={person.address}>
                  <div className="access-item-main">
                    <strong>{person.address}</strong>
                    <ul className="record-facts">
                      {person.display_name ? <li>{person.display_name}</li> : null}
                      <li>{rightsText(person.rights)}</li>
                    </ul>
                  </div>
                  <div className="record-actions">
                    <Button onClick={() => setSharing({ open: true, existing: person })}>
                      Change<span className="visually-hidden"> what {person.address} may do</span>
                    </Button>
                    <Button data-variant="destructive" pendingAnnouncement="Removing" onClick={() => unshare(person)}>
                      Remove<span className="visually-hidden"> {person.address}</span>
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Section>

        <Section
          title="You have access to"
          description="Other mailboxes you may use with your own sign-in, besides your own."
        >
          {me.access.length === 0 ? (
            <p className="muted account-empty">
              No other mailboxes. The people who manage your mail can give you access to a shared mailbox, and others
              can share their mailbox with you.
            </p>
          ) : (
            <ul className="access-list" aria-label="Mailboxes you have access to">
              {me.access.map((access) => (
                <li className="access-item" key={access.address}>
                  <div className="access-item-main">
                    <strong>
                      {access.address}
                      {access.shared ? <StatusBadge tone="neutral">Shared</StatusBadge> : null}
                    </strong>
                    <ul className="record-facts">
                      {access.display_name ? <li>{access.display_name}</li> : null}
                      <li>{accessKindText(access)}</li>
                      <li>{rightsText(access.rights)}</li>
                    </ul>
                    <p className="muted account-folder">{folderText(access)}</p>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </Section>

        <Section
          title="Settings for mail programs"
          description="Enter these in a mail program or on a phone to read and send your mail."
        >
          <dl className="account-client">
            {clientRows(me.client, me.address, me.app_passwords_only).map((row) => (
              <div key={row.label}>
                <dt>{row.label}</dt>
                <dd>
                  {row.copy ? <code>{row.value}</code> : <span>{row.value}</span>}
                  {row.copy ? (
                    <Button className="copy-button" onClick={() => copyText(row.value)}>
                      Copy<span className="visually-hidden"> {row.label.toLowerCase()}</span>
                    </Button>
                  ) : null}
                </dd>
              </div>
            ))}
          </dl>
          <div className="account-apple">
            <p className="muted">
              On an iPhone, iPad or Mac, download the setup and open it; it asks for the password when you install it.
            </p>
            <Button className="account-download" render={<a href={APPLE_SETUP_URL} download />}>
              Download setup for Apple devices
            </Button>
          </div>
        </Section>

        <footer className="account-footer muted">The people who manage this server can reset your password.</footer>
      </main>

      <DisplayNameDialog
        open={dialog === 'name'}
        current={me.display_name}
        onOpenChange={close}
        onSubmit={saveName}
      />
      <ChangePasswordDialog
        open={dialog === 'password'}
        hasAppPasswords={me.app_passwords.length > 0}
        onOpenChange={close}
        onSubmit={savePassword}
      />
      <CreateAppPasswordDialog open={dialog === 'app'} onOpenChange={close} onSubmit={addAppPassword} />
      <NewAppPasswordDialog created={created} onClose={() => setCreated(null)} />
      <ShareDialog
        open={sharing.open}
        own={me.address}
        existing={sharing.existing}
        onOpenChange={(open) => setSharing((current) => ({ ...current, open }))}
        onSubmit={saveShare}
      />
    </div>
  )
}
