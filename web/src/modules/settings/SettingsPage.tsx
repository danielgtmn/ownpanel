import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { useState, type ReactNode } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import {
  AddressPicker,
  ADDRESSES_PURPOSE,
  announceSaved,
  CERTIFICATE_EMAIL_LABEL,
  CertificateEmailForm,
  getSettings,
  initialAddressSelection,
  LOCKED_NOTE,
  PanelAddressDetails,
  PanelDomainForm,
  sameAddresses,
  saveSettings,
  type PanelSettings,
} from '../../shared/settings/index.ts'
import { ErrorState, FormDialog, LoadingState, PageHeader, Section } from '../../shared/ui/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { ConnectedAccountsSection } from './ConnectedAccountsSection.tsx'
import { GitHubSection } from './GitHubSection.tsx'
import { ipAccessNotice } from './rules.ts'
import { MailSection } from './MailSection.tsx'
import { UpdatesSection } from './UpdatesSection.tsx'

type Saved = (next: PanelSettings) => void

function ChangeButton({ what, onClick }: { what: string; onClick: () => void }) {
  return (
    <Button onClick={onClick}>
      Change<span className="visually-hidden"> {what}</span>
    </Button>
  )
}

/** The current value of a setting: shown on the page, changed in a dialog. */
function Current({ label, locked, children }: { label: string; locked: boolean; children: ReactNode }) {
  return (
    <div className="setting-current">
      <span className="visually-hidden">{label}: </span>
      {children}
      {locked ? <p className="muted">{LOCKED_NOTE}, so it can't be changed here.</p> : null}
    </div>
  )
}

function AddressesForm({ settings, onSaved, onDone }: { settings: PanelSettings; onSaved: Saved; onDone: () => void }) {
  const [selected, setSelected] = useState(() => initialAddressSelection(settings))
  const [extra, setExtra] = useState<string[]>([])
  const [error, setError] = useState<string | undefined>()

  async function save() {
    if (selected.length === 0) {
      setError('Choose at least one address, or add one.')
      return
    }
    if (sameAddresses(selected, settings.server_ips)) {
      onDone()
      return
    }
    try {
      const result = await saveSettings({ server_ips: selected })
      onSaved(result.settings)
      announceSaved(result, 'Saved the server addresses.')
      onDone()
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <form className="form" onSubmit={(event) => event.preventDefault()} noValidate>
      <AddressPicker
        settings={settings}
        selected={selected}
        onSelectedChange={(next) => {
          setSelected(next)
          setError(undefined)
        }}
        extra={extra}
        onExtraChange={setExtra}
        error={error}
      />
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" pendingAnnouncement="Saving" onClick={save}>
          Save addresses
        </Button>
      </div>
    </form>
  )
}

function AddressesSection({ settings, onSaved }: { settings: PanelSettings; onSaved: Saved }) {
  const [open, setOpen] = useState(false)
  const locked = settings.server_ips_locked
  return (
    <Section
      title="Server addresses"
      description={`The IP addresses of this server. ${ADDRESSES_PURPOSE}`}
      actions={locked ? null : <ChangeButton what="the server addresses" onClick={() => setOpen(true)} />}
    >
      <Current label="Server addresses" locked={locked}>
        {settings.server_ips.length > 0 ? (
          <ul className="setting-values" aria-label="Server addresses">
            {settings.server_ips.map((ip) => (
              <li key={ip}>
                <code>{ip}</code>
              </li>
            ))}
          </ul>
        ) : (
          <p className="notice">No address chosen yet. Choose this server's public addresses so domains can point here.</p>
        )}
      </Current>
      <FormDialog
        open={open}
        onOpenChange={setOpen}
        title="Server addresses"
        description={`Choose the addresses that are reachable from the internet. ${ADDRESSES_PURPOSE}`}
      >
        <AddressesForm settings={settings} onSaved={onSaved} onDone={() => setOpen(false)} />
      </FormDialog>
    </Section>
  )
}

function PanelAddressSection({
  settings,
  onSaved,
  open,
  onOpenChange,
}: {
  settings: PanelSettings
  onSaved: Saved
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const locked = settings.panel_domain_locked
  return (
    <Section
      title="Panel address"
      description="The domain the panel is opened at. With a domain, the panel gets a trusted certificate and browsers don't warn."
      actions={
        locked ? null : (
          <ChangeButton what="the panel address" onClick={() => onOpenChange(true)} />
        )
      }
    >
      <div className="settings-stack">
        <Current label="Panel domain" locked={locked}>
          {settings.panel_domain ? (
            <p className="setting-value">
              <code>{settings.panel_domain}</code>
            </p>
          ) : (
            <p className="muted">No domain. The panel is reached by this server's IP address.</p>
          )}
        </Current>
        <PanelAddressDetails settings={settings} />
      </div>
      <FormDialog
        open={open}
        onOpenChange={onOpenChange}
        title="Panel address"
        description="Choose the domain you'll open the panel at. Then point it to this server."
      >
        <PanelDomainForm
          settings={settings}
          onSaved={onSaved}
          askBeforeChange
          allowEmpty
          onDone={() => onOpenChange(false)}
        />
      </FormDialog>
    </Section>
  )
}

function CertificateSection({ settings, onSaved }: { settings: PanelSettings; onSaved: Saved }) {
  const [open, setOpen] = useState(false)
  const locked = settings.acme_email_locked
  return (
    <Section
      title="Certificate notices"
      description="The service that issues the certificates may write to this address about them, for example before one expires."
      actions={locked ? null : <ChangeButton what="the e-mail for certificate notices" onClick={() => setOpen(true)} />}
    >
      <Current label={CERTIFICATE_EMAIL_LABEL} locked={locked}>
        {settings.acme_email ? (
          <p className="setting-value">{settings.acme_email}</p>
        ) : (
          <p className="muted">No e-mail set. Certificates work without one.</p>
        )}
      </Current>
      <FormDialog
        open={open}
        onOpenChange={setOpen}
        title="Certificate notices"
        description="Where notices about the certificates of the panel and your sites go."
      >
        <CertificateEmailForm settings={settings} onSaved={onSaved} onDone={() => setOpen(false)} />
      </FormDialog>
    </Section>
  )
}

function Loaded({ initial }: { initial: PanelSettings }) {
  const [settings, setSettings] = useState(initial)
  const [panelOpen, setPanelOpen] = useState(false)
  const notice = ipAccessNotice(settings)

  return (
    <>
      <PageHeader title="Settings" description="Your connected accounts, GitHub, and how people reach this panel, the sites and the mail on this server." />
      {notice ? (
        <div className="notice settings-notice">
          <p>{notice}</p>
          {settings.panel_domain_locked ? null : (
            <Button onClick={() => setPanelOpen(true)}>Set panel domain</Button>
          )}
        </div>
      ) : null}
      <ConnectedAccountsSection />
      <GitHubSection onSetPanelDomain={settings.panel_domain_locked ? undefined : () => setPanelOpen(true)} />
      <AddressesSection settings={settings} onSaved={setSettings} />
      <PanelAddressSection settings={settings} onSaved={setSettings} open={panelOpen} onOpenChange={setPanelOpen} />
      <CertificateSection settings={settings} onSaved={setSettings} />
      <MailSection settings={settings} onSaved={setSettings} />
      <UpdatesSection />
    </>
  )
}

function MemberSettings() {
  return (
    <>
      <PageHeader title="Settings" description="The accounts this panel may use for you." />
      <ConnectedAccountsSection />
    </>
  )
}

function ServerSettings() {
  const settings = useResource(getSettings, 'settings')

  if (settings.status === 'loading') return <LoadingState label="Loading settings…" />
  if (settings.status === 'error') {
    return <ErrorState title="Couldn't load the settings" message={errorMessage(settings.error)} onRetry={settings.retry} />
  }
  return <Loaded initial={settings.data} />
}

export function SettingsPage() {
  const { user } = useSession()
  usePageTitle('Settings')
  return user.role === 'admin' ? <ServerSettings /> : <MemberSettings />
}
