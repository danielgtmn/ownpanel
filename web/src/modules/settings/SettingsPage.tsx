import { Button } from '@ui-sdk/react/button'
import { useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import {
  AddressPicker,
  ADDRESSES_PURPOSE,
  announceSaved,
  CertificateEmailForm,
  getSettings,
  initialAddressSelection,
  PanelAddressDetails,
  PanelDomainForm,
  sameAddresses,
  saveSettings,
  type PanelSettings,
} from '../../shared/settings/index.ts'
import { ErrorState, LoadingState, PageHeader, Section } from '../../shared/ui/index.ts'
import { ipAccessNotice } from './rules.ts'

function AddressesSection({ settings, onSaved }: { settings: PanelSettings; onSaved: (next: PanelSettings) => void }) {
  const [selected, setSelected] = useState(() => initialAddressSelection(settings))
  const [extra, setExtra] = useState<string[]>([])
  const [error, setError] = useState<string | undefined>()
  const unchanged = sameAddresses(selected, settings.server_ips)

  async function save() {
    if (selected.length === 0) {
      setError('Choose at least one address, or add one.')
      return
    }
    try {
      const result = await saveSettings({ server_ips: selected })
      onSaved(result.settings)
      setSelected(result.settings.server_ips)
      setExtra([])
      announceSaved(result, 'Saved the server addresses.')
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <Section title="Server addresses" description={`The IP addresses of this server. ${ADDRESSES_PURPOSE}`}>
      <div className="settings-stack">
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
        {settings.server_ips_locked ? null : (
          <div className="settings-actions">
            <Button data-variant="solid" pendingAnnouncement="Saving" disabled={unchanged} onClick={save}>
              Save addresses
            </Button>
          </div>
        )}
      </div>
    </Section>
  )
}

function Loaded({ initial }: { initial: PanelSettings }) {
  const [settings, setSettings] = useState(initial)
  const notice = ipAccessNotice(settings)

  return (
    <>
      <PageHeader title="Settings" description="How people reach this panel and the sites on this server." />
      {notice ? <p className="notice settings-notice">{notice}</p> : null}
      <AddressesSection settings={settings} onSaved={setSettings} />
      <Section
        title="Panel address"
        description="The domain the panel is opened at. With a domain, the panel gets a trusted certificate and browsers don't warn."
      >
        <div className="settings-stack">
          <PanelDomainForm settings={settings} onSaved={setSettings} askBeforeChange allowEmpty />
          <PanelAddressDetails settings={settings} />
        </div>
      </Section>
      <Section title="Certificate notices">
        <CertificateEmailForm settings={settings} onSaved={setSettings} />
      </Section>
    </>
  )
}

export function SettingsPage() {
  const settings = useResource(getSettings, 'settings')
  usePageTitle('Settings')

  if (settings.status === 'loading') return <LoadingState label="Loading settings…" />
  if (settings.status === 'error') {
    return <ErrorState title="Couldn't load the settings" message={errorMessage(settings.error)} onRetry={settings.retry} />
  }
  return <Loaded initial={settings.data} />
}
