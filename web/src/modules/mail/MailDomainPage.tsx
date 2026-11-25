import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Meter, MeterIndicator, MeterTrack } from '@ui-sdk/react/meter'
import { toast } from '@ui-sdk/react/toast'
import { useId, useState, type FormEvent } from 'react'
import { errorMessage, isNotFound, isSavedButNotApplied } from '../../shared/api/index.ts'
import { formatDateTime } from '../../shared/format/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { hrefFor, navigate } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import {
  ArrowLeftIcon,
  copyText,
  EmptyState,
  ErrorState,
  FormDialog,
  LoadingState,
  PageHeader,
  Section,
  StatusBadge,
} from '../../shared/ui/index.ts'
import { AliasDialog } from './AliasDialog.tsx'
import {
  activateMailDomain,
  appleProfileHref,
  checkMailDomain,
  createAlias,
  createAppPassword,
  createMailbox,
  deleteAlias,
  deleteMailbox,
  deleteMailDomain,
  exportHref,
  getMailDomain,
  getMailStatus,
  listAliases,
  listMailboxes,
  setMailboxLimits,
  setMailboxPassword,
  unblockMailbox,
  updateAlias,
  updateMailbox,
} from './api.ts'
import { ImportDialog } from './ImportDialog.tsx'
import { MailboxAppPasswords } from './MailboxAppPasswords.tsx'
import {
  CreateAppPasswordDialog,
  CreateMailboxDialog,
  DisplayNameDialog,
  MailboxLimitsDialog,
  MailboxPasswordDialog,
  NewAppPasswordDialog,
} from './MailboxDialogs.tsx'
import { MailboxImports } from './MailboxImports.tsx'
import { MailboxRulesPanel } from './MailboxRulesPanel.tsx'
import { OrganisationSection } from './OrganisationSection.tsx'
import { RecordsSection } from './RecordsSection.tsx'
import { aliasTitle, clientRows, domainState, sendLimitsText, sortAliases, usageShare, usageText } from './rules.ts'
import type { LimitsChange, MailAlias, MailDomain, MailImport, Mailbox, MailStatus } from './types.ts'

// A change that is saved but not yet passed on to the mail server is not a failure for the person.
async function saved(run: () => Promise<unknown>, success: string) {
  try {
    await run()
    toast.success(success)
  } catch (error) {
    if (!isSavedButNotApplied(error)) throw error
    toast.warning(errorMessage(error))
  }
}

async function tryTo(run: () => Promise<void>) {
  try {
    await run()
  } catch (error) {
    toast.error(errorMessage(error))
  }
}

// ---- status ----

function StatusSection({
  mail,
  isAdmin,
  serverNamed,
  onChecked,
}: {
  mail: MailDomain
  isAdmin: boolean
  serverNamed: boolean
  onChecked: (next: MailDomain) => void
}) {
  const state = domainState(mail)
  async function activate() {
    await tryTo(async () => {
      const next = await activateMailDomain(mail.domain)
      onChecked(next)
      toast.success(`${mail.domain} now receives mail here.`)
    })
  }
  async function check() {
    await tryTo(async () => {
      const next = await checkMailDomain(mail.domain)
      onChecked(next)
      if (next.active) toast.success(`${mail.domain} now receives mail here.`)
      else toast.info('Not yet. The records are checked again every few minutes.')
    })
  }
  if (mail.active) return null
  return (
    <Section
      title="Waiting for the mail records"
      actions={
        <Button pendingAnnouncement="Checking" onClick={check}>
          Check now
        </Button>
      }
    >
      <div className="mail-waiting">
        <p>{state.sentence}</p>
        {mail.problem && serverNamed ? <p className="notice">{mail.problem}</p> : null}
        {mail.checked_at ? <p className="muted">Last checked {formatDateTime(mail.checked_at)}.</p> : null}
        {isAdmin ? (
          <p className="muted">
            Mail reaches this server another way, for example through a gateway?{' '}
            <Button pendingAnnouncement="Turning on" onClick={activate}>
              Receive mail here now
            </Button>
          </p>
        ) : null}
      </div>
    </Section>
  )
}

// ---- mailboxes ----

function MailboxRow({
  mailbox,
  isAdmin,
  importsVersion,
  appsVersion,
  onPassword,
  onDisplayName,
  onLimits,
  onCreateAppPassword,
  onMailboxChanged,
  onImport,
  onChooseFolders,
  onImportsSettled,
  onDownload,
  onUnblock,
  onDelete,
}: {
  mailbox: Mailbox
  isAdmin: boolean
  importsVersion: number
  appsVersion: number
  onPassword: (mailbox: Mailbox) => void
  onDisplayName: (mailbox: Mailbox) => void
  onLimits: (mailbox: Mailbox) => void
  onCreateAppPassword: (mailbox: Mailbox) => void
  onMailboxChanged: () => Promise<void>
  onImport: (mailbox: Mailbox) => void
  onChooseFolders: (mailbox: Mailbox, job: MailImport) => void
  onImportsSettled: () => void
  onDownload: (mailbox: Mailbox) => Promise<void>
  onUnblock: (mailbox: Mailbox) => Promise<void>
  onDelete: (mailbox: Mailbox) => Promise<void>
}) {
  const share = usageShare(mailbox)
  const blocked = mailbox.usage?.blocked ?? false
  const [appsOpen, setAppsOpen] = useState(false)
  const appsId = useId()
  const [rulesOpen, setRulesOpen] = useState(false)
  const rulesId = useId()
  const away = mailbox.usage?.out_of_office ?? false
  return (
    <li className="record-row">
      <div className="record-main mail-mailbox-main">
        <h3 className="record-title">
          {mailbox.address}
          {blocked ? (
            <>
              {' '}
              <StatusBadge tone="warning">Can't send</StatusBadge>
            </>
          ) : null}
          {away ? (
            <>
              {' '}
              <StatusBadge tone="neutral">Out of office</StatusBadge>
            </>
          ) : null}
        </h3>
        {blocked ? (
          <p className="mail-blocked">
            {mailbox.usage?.blocked_reason || 'The mail server stopped this mailbox from sending.'}
            {isAdmin ? '' : ' An administrator can let it send again.'}
          </p>
        ) : null}
        <div className="mail-usage">
          {share !== null ? (
            <Meter value={Math.round(share * 100)} aria-label={`Storage used by ${mailbox.address}`}>
              <MeterTrack>
                <MeterIndicator />
              </MeterTrack>
            </Meter>
          ) : null}
          <span className="muted">{usageText(mailbox)}</span>
        </div>
        <ul className="record-facts">
          {mailbox.display_name ? <li>Display name: {mailbox.display_name}</li> : null}
          <li>{sendLimitsText(mailbox)}</li>
          {mailbox.app_passwords_only ? <li>Mail programs sign in with app passwords only</li> : null}
          {isAdmin && mailbox.owner ? <li>Owner: {mailbox.owner}</li> : null}
        </ul>
      </div>
      <div className="record-actions">
        <Button onClick={() => onPassword(mailbox)}>
          Set password<span className="visually-hidden"> of {mailbox.address}</span>
        </Button>
        <Button onClick={() => onDisplayName(mailbox)}>
          Display name<span className="visually-hidden"> of {mailbox.address}</span>
        </Button>
        <Button onClick={() => onLimits(mailbox)}>
          Limits<span className="visually-hidden"> of {mailbox.address}</span>
        </Button>
        <Button aria-expanded={appsOpen} aria-controls={appsId} onClick={() => setAppsOpen((open) => !open)}>
          {appsOpen ? 'Hide app passwords' : 'App passwords'}
          <span className="visually-hidden"> of {mailbox.address}</span>
        </Button>
        <Button aria-expanded={rulesOpen} aria-controls={rulesId} onClick={() => setRulesOpen((open) => !open)}>
          {rulesOpen ? 'Hide out-of-office and forwarding' : 'Out-of-office and forwarding'}
          <span className="visually-hidden"> of {mailbox.address}</span>
        </Button>
        <Button onClick={() => onImport(mailbox)}>
          Import mail<span className="visually-hidden"> into {mailbox.address}</span>
        </Button>
        <Button onClick={() => onDownload(mailbox)}>
          Download all mail<span className="visually-hidden"> of {mailbox.address}</span>
        </Button>
        <Button className="mail-download" render={<a href={appleProfileHref(mailbox.id)} download />}>
          Download setup for Apple devices<span className="visually-hidden"> for {mailbox.address}</span>
        </Button>
        {isAdmin && blocked ? (
          <Button pendingAnnouncement="Allowing" onClick={() => onUnblock(mailbox)}>
            Allow sending<span className="visually-hidden"> from {mailbox.address}</span>
          </Button>
        ) : null}
        <Button data-variant="destructive" pendingAnnouncement="Deleting" onClick={() => onDelete(mailbox)}>
          Delete<span className="visually-hidden"> {mailbox.address}</span>
        </Button>
      </div>
      {appsOpen ? (
        <div id={appsId} className="mail-apps-panel">
          <h4>App passwords</h4>
          <MailboxAppPasswords
            mailbox={mailbox}
            refreshKey={appsVersion}
            onCreate={onCreateAppPassword}
            onMailboxChanged={onMailboxChanged}
          />
        </div>
      ) : null}
      {rulesOpen ? (
        <div id={rulesId} className="mail-apps-panel">
          <h4>Out-of-office and forwarding</h4>
          <MailboxRulesPanel mailbox={mailbox} onChanged={() => void onMailboxChanged()} />
        </div>
      ) : null}
      <MailboxImports
        target={mailbox}
        refreshKey={importsVersion}
        onChoose={(job) => onChooseFolders(mailbox, job)}
        onSettled={onImportsSettled}
      />
    </li>
  )
}

// startDownload saves a file the server sends, like a click on a link.
function startDownload(href: string) {
  const link = document.createElement('a')
  link.href = href
  link.download = ''
  document.body.append(link)
  link.click()
  link.remove()
}

function MailboxesSection({ domain, onChange }: { domain: string; onChange: () => void }) {
  const mailboxes = useResource(() => listMailboxes(domain), `mailboxes:${domain}`)
  const { isAdmin } = useSession()
  const [creating, setCreating] = useState(false)
  const [passwordFor, setPasswordFor] = useState<Mailbox | null>(null)
  const [limitsFor, setLimitsFor] = useState<Mailbox | null>(null)
  const [nameFor, setNameFor] = useState<Mailbox | null>(null)
  const [appFor, setAppFor] = useState<Mailbox | null>(null)
  // The new app password, shown once; dropped when its dialog closes.
  const [newApp, setNewApp] = useState<{ address: string; name: string; password: string } | null>(null)
  const [appsVersion, setAppsVersion] = useState(0)
  const [importInto, setImportInto] = useState<{ mailbox: Mailbox; ready: MailImport | null } | null>(null)
  const [importOpen, setImportOpen] = useState(false)
  const [importsVersion, setImportsVersion] = useState(0)

  function openImport(mailbox: Mailbox, ready: MailImport | null) {
    setImportInto({ mailbox, ready })
    setImportOpen(true)
  }

  async function download(mailbox: Mailbox) {
    const yes = await confirm({
      title: `Download all mail of ${mailbox.address}?`,
      description:
        'You get one .zip file with every folder of the mailbox. It can be imported here again or opened with mail programs that read .mbox files. The download is noted in the activity log.',
      confirmLabel: 'Download',
      cancelLabel: 'Cancel',
    })
    if (yes) startDownload(exportHref(mailbox.domain, mailbox.local_part))
  }

  async function refresh() {
    await mailboxes.reload()
    onChange()
  }

  async function create(localPart: string, password: string, displayName: string) {
    await saved(() => createMailbox(domain, localPart, password, displayName), `${localPart}@${domain} was created.`)
    setCreating(false)
    await refresh()
  }

  async function setDisplayName(name: string) {
    if (!nameFor) return
    if (name !== nameFor.display_name) {
      await saved(
        () => updateMailbox(nameFor.id, { display_name: name }),
        name ? `${nameFor.address} now sends as “${name}”.` : `${nameFor.address} no longer shows a display name.`,
      )
    }
    setNameFor(null)
    await refresh()
  }

  async function createApp(name: string) {
    if (!appFor) return
    const mailbox = appFor
    // Errors stay in the form. The server always answers a saved app password with its password.
    const created = await createAppPassword(mailbox.id, name)
    setAppFor(null)
    setNewApp({ address: mailbox.address, name: created.name, password: created.password })
    setAppsVersion((n) => n + 1)
  }

  async function setPassword(password: string) {
    if (!passwordFor) return
    await saved(() => setMailboxPassword(passwordFor.id, password), `The password of ${passwordFor.address} was changed.`)
    setPasswordFor(null)
  }

  async function setLimits(change: LimitsChange) {
    if (!limitsFor) return
    if (Object.keys(change).length > 0) {
      await saved(() => setMailboxLimits(limitsFor.id, change), `The limits of ${limitsFor.address} were changed.`)
    }
    setLimitsFor(null)
    await refresh()
  }

  async function unblock(mailbox: Mailbox) {
    await tryTo(() => saved(() => unblockMailbox(mailbox.id), `${mailbox.address} can send again.`))
    await refresh()
  }

  async function remove(mailbox: Mailbox) {
    const yes = await confirm({
      title: `Delete ${mailbox.address}?`,
      description: "All mail stored in this mailbox is deleted, and mail to the address is turned away. This can't be undone.",
      confirmLabel: 'Delete mailbox',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    await tryTo(() => saved(() => deleteMailbox(mailbox.id), `${mailbox.address} was deleted.`))
    await refresh()
  }

  const addButton = (
    <Button data-variant="solid" onClick={() => setCreating(true)}>
      Create mailbox
    </Button>
  )
  const list = mailboxes.status === 'ready' ? mailboxes.data : []

  return (
    <Section title="Mailboxes" description="Each mailbox keeps the mail of one address." actions={list.length > 0 ? addButton : null}>
      {mailboxes.status === 'loading' ? <LoadingState label="Loading mailboxes…" /> : null}
      {mailboxes.status === 'error' ? (
        <ErrorState title="Couldn't load the mailboxes" message={errorMessage(mailboxes.error)} onRetry={mailboxes.retry} />
      ) : null}
      {mailboxes.status === 'ready' && list.length === 0 ? (
        <EmptyState inSection title="No mailboxes yet" description={`Create one, like info@${domain}.`} action={addButton} />
      ) : null}
      {list.length > 0 ? (
        <ul className="record-list" aria-label={`Mailboxes on ${domain}`}>
          {list.map((mailbox) => (
            <MailboxRow
              key={mailbox.id}
              mailbox={mailbox}
              isAdmin={isAdmin}
              importsVersion={importsVersion}
              appsVersion={appsVersion}
              onPassword={setPasswordFor}
              onDisplayName={setNameFor}
              onLimits={setLimitsFor}
              onCreateAppPassword={setAppFor}
              onMailboxChanged={refresh}
              onImport={(box) => openImport(box, null)}
              onChooseFolders={openImport}
              onImportsSettled={() => void mailboxes.reload()}
              onDownload={download}
              onUnblock={unblock}
              onDelete={remove}
            />
          ))}
        </ul>
      ) : null}
      <CreateMailboxDialog open={creating} onOpenChange={setCreating} domain={domain} onSubmit={create} />
      <MailboxPasswordDialog mailbox={passwordFor} onClose={() => setPasswordFor(null)} onSubmit={setPassword} />
      <MailboxLimitsDialog mailbox={limitsFor} isAdmin={isAdmin} onClose={() => setLimitsFor(null)} onSubmit={setLimits} />
      <DisplayNameDialog mailbox={nameFor} onClose={() => setNameFor(null)} onSubmit={setDisplayName} />
      <CreateAppPasswordDialog mailbox={appFor} onClose={() => setAppFor(null)} onSubmit={createApp} />
      <NewAppPasswordDialog created={newApp} onClose={() => setNewApp(null)} />
      <ImportDialog
        key={importInto ? `${importInto.mailbox.id}:${importInto.ready?.id ?? 'new'}` : 'none'}
        target={importInto?.mailbox ?? null}
        ready={importInto?.ready ?? null}
        open={importOpen}
        onOpenChange={(open) => {
          setImportOpen(open)
          if (!open) setImportsVersion((n) => n + 1)
        }}
        onStarted={() => setImportsVersion((n) => n + 1)}
      />
    </Section>
  )
}

// ---- forwarding ----

function AliasesSection({ domain, onChange }: { domain: string; onChange: () => void }) {
  const aliases = useResource(() => listAliases(domain), `aliases:${domain}`)
  const [dialog, setDialog] = useState<{ open: boolean; existing: MailAlias | null }>({ open: false, existing: null })

  async function refresh() {
    await aliases.reload()
    onChange()
  }

  async function save(localPart: string, targets: string[]) {
    const existing = dialog.existing
    const label = localPart === '*' ? `Mail to all other addresses on ${domain}` : `Mail to ${localPart}@${domain}`
    await saved(
      () => (existing ? updateAlias(existing.id, localPart, targets) : createAlias(domain, localPart, targets)),
      `${label} now goes to ${targets.join(', ')}.`,
    )
    setDialog((current) => ({ ...current, open: false }))
    await refresh()
  }

  async function remove(alias: MailAlias) {
    const yes = await confirm({
      title: `Delete the forwarding of ${aliasTitle(alias)}?`,
      description: 'Mail to this address is no longer passed on and is turned away, unless a mailbox has the address.',
      confirmLabel: 'Delete forwarding',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    await tryTo(() => saved(() => deleteAlias(alias.id), 'The forwarding was deleted.'))
    await refresh()
  }

  const addButton = <Button onClick={() => setDialog({ open: true, existing: null })}>Add forwarding</Button>
  const list = aliases.status === 'ready' ? sortAliases(aliases.data) : []

  return (
    <Section
      title="Forwarding"
      description="Addresses without a mailbox that pass mail on to other addresses."
      actions={list.length > 0 ? addButton : null}
    >
      {aliases.status === 'loading' ? <LoadingState label="Loading forwarding…" /> : null}
      {aliases.status === 'error' ? (
        <ErrorState title="Couldn't load the forwarding" message={errorMessage(aliases.error)} onRetry={aliases.retry} />
      ) : null}
      {aliases.status === 'ready' && list.length === 0 ? (
        <EmptyState
          inSection
          title="No forwarding yet"
          description={`Pass mail to sales@${domain} or to every unknown address on to a mailbox.`}
          action={addButton}
        />
      ) : null}
      {list.length > 0 ? (
        <ul className="record-list" aria-label={`Forwarding on ${domain}`}>
          {list.map((alias) => (
            <li className="record-row" key={alias.id}>
              <div className="record-main">
                <h3 className="record-title">{aliasTitle(alias)}</h3>
                <p className="muted mail-targets">Goes to {alias.targets.join(', ')}</p>
              </div>
              <div className="record-actions">
                <Button onClick={() => setDialog({ open: true, existing: alias })}>
                  Edit<span className="visually-hidden"> {aliasTitle(alias)}</span>
                </Button>
                <Button data-variant="destructive" pendingAnnouncement="Deleting" onClick={() => remove(alias)}>
                  Delete<span className="visually-hidden"> {aliasTitle(alias)}</span>
                </Button>
              </div>
            </li>
          ))}
        </ul>
      ) : null}
      <AliasDialog
        open={dialog.open}
        onOpenChange={(open) => setDialog((current) => ({ ...current, open }))}
        domain={domain}
        existing={dialog.existing}
        catchAllTaken={list.some((alias) => alias.catch_all)}
        onSubmit={save}
      />
    </Section>
  )
}

// ---- settings for mail programs ----

function ClientSection({ data }: { data: MailStatus }) {
  if (!data.client.server) return null
  return (
    <Section
      title="Settings for mail programs"
      description="Enter these in a mail program or on a phone to read and send mail of a mailbox."
    >
      <dl className="mail-client">
        {clientRows(data.client).map((row) => (
          <div key={row.label}>
            <dt>{row.label}</dt>
            <dd>
              {row.copy ? <code>{row.value}</code> : <span>{row.value}</span>}
              {row.copy ? (
                <Button className="copy-button" onClick={() => copyText(row.value)}>
                  Copy<span className="visually-hidden"> the server name</span>
                </Button>
              ) : null}
            </dd>
          </div>
        ))}
      </dl>
      <p className="muted mail-client-auto">
        Most mail programs fill these in by themselves from the address once the domain's recommended records are set
        up. On an iPhone, iPad or Mac, open the mailbox's setup for Apple devices instead; it asks for the password when
        you install it.
      </p>
      {data.problem ? <p className="notice mail-client-problem">{data.problem}</p> : null}
    </Section>
  )
}

// ---- delete ----

function DeleteForm({ domain, onDelete }: { domain: string; onDelete: (confirm: string) => Promise<void> }) {
  const [typed, setTyped] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const matches = typed.trim().toLowerCase() === domain

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!matches) return
    setBusy(true)
    setError(null)
    try {
      await onDelete(typed.trim().toLowerCase())
    } catch (failure) {
      setError(errorMessage(failure))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="confirm">
        <FieldLabel>Type {domain} to confirm</FieldLabel>
        <Input
          value={typed}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          autoFocus
          onValueChange={(value) => {
            setTyped(value)
            setError(null)
          }}
        />
        <FieldDescription>The Delete button works once the name matches.</FieldDescription>
      </Field>
      {error ? (
        <p className="form-error" role="alert">
          {error}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="destructive" disabled={!matches || busy}>
          {busy ? 'Deleting…' : 'Delete e-mail'}
        </Button>
      </div>
    </form>
  )
}

function DangerSection({ mail }: { mail: MailDomain }) {
  const [open, setOpen] = useState(false)

  async function remove(typed: string) {
    try {
      await deleteMailDomain(mail.domain, typed)
      toast.success(`E-mail for ${mail.domain} was deleted.`)
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setOpen(false)
    navigate('mail')
  }

  return (
    <Section
      title="Delete e-mail for this domain"
      tone="danger"
      description="Deletes every mailbox with all its stored mail and every forwarding of the domain."
      actions={
        <Button data-variant="destructive" onClick={() => setOpen(true)}>
          Delete e-mail
        </Button>
      }
    >
      <FormDialog
        open={open}
        onOpenChange={setOpen}
        title={`Delete e-mail for ${mail.domain}?`}
        description={`Every mailbox of ${mail.domain} (now ${mail.mailboxes}) with all its stored mail, and every forwarding, is deleted. Mail to its addresses is turned away. This can't be undone.`}
      >
        <DeleteForm domain={mail.domain} onDelete={remove} />
      </FormDialog>
    </Section>
  )
}

// ---- page ----

export function MailDomainPage({ domain }: { domain: string }) {
  const { isAdmin } = useSession()
  const resource = useResource(() => getMailDomain(domain), `mail-domain:${domain}`)
  const status = useResource(getMailStatus, 'mail-status')
  const [checked, setChecked] = useState<MailDomain | null>(null)
  usePageTitle(`E-mail of ${domain}`)

  const back = (
    <a className="inline-link" href={hrefFor('mail')}>
      <ArrowLeftIcon />
      All e-mail
    </a>
  )

  if (resource.status === 'loading') return <LoadingState label="Loading e-mail…" />
  if (resource.status === 'error') {
    if (isNotFound(resource.error)) {
      return (
        <EmptyState
          title="E-mail is not turned on here"
          description={`E-mail is not turned on for ${domain}, or you can't see it.`}
          action={<a href={hrefFor('mail')}>Show all e-mail</a>}
        />
      )
    }
    return <ErrorState title="Couldn't load the e-mail" message={errorMessage(resource.error)} onRetry={resource.retry} />
  }

  const mail = checked && checked.domain === resource.data.domain && !resource.data.active ? checked : resource.data
  // Until the status is known, assume the server has a name rather than flashing a warning.
  const serverNamed = status.status !== 'ready' || Boolean(status.data.client.server)
  const state = domainState(mail)
  const reload = () => {
    setChecked(null)
    void resource.reload()
  }

  return (
    <>
      <PageHeader
        back={back}
        title={mail.domain}
        description="E-mail: mailboxes and forwarding for addresses on this domain."
        meta={<StatusBadge tone={state.tone}>{state.label}</StatusBadge>}
      />
      {serverNamed ? null : (
        <div className="notice mail-unnamed" role="status">
          <p>
            E-mail can't work yet: the mail server has no name.{' '}
            {isAdmin ? 'Set one in the settings.' : 'An administrator sets it in the settings.'}
          </p>
          {isAdmin ? <a href={hrefFor('settings')}>Open settings</a> : null}
        </div>
      )}
      <StatusSection
        mail={mail}
        isAdmin={isAdmin}
        serverNamed={serverNamed}
        onChecked={(next) => {
          if (next.active) reload()
          else setChecked(next)
        }}
      />
      <MailboxesSection domain={mail.domain} onChange={reload} />
      <AliasesSection domain={mail.domain} onChange={reload} />
      <OrganisationSection mail={mail} onChange={reload} />
      {serverNamed ? <RecordsSection domain={mail.domain} onApplied={reload} /> : null}
      {status.status === 'ready' ? <ClientSection data={status.data} /> : null}
      <DangerSection mail={mail} />
    </>
  )
}
