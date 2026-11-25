import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../api/index.ts'
import { StatusBadge } from '../ui/index.ts'
import type { PanelSettings } from './api.ts'
import { LockedField } from './LockedField.tsx'
import {
  certificateText,
  certificateTone,
  domainChangeQuestion,
  dnsText,
  dnsTone,
  normalizeDomain,
  recordRows,
  validateDomain,
} from './rules.ts'
import { announceSaved, copyText, saveSettings } from './save.ts'
import { usePanelChecks } from './usePanelChecks.ts'

function CopyButton({ text, what }: { text: string; what: string }) {
  return (
    <Button className="copy-button" onClick={() => copyText(text)}>
      Copy<span className="visually-hidden"> {what}</span>
    </Button>
  )
}

export function DnsRecords({ domain, ips }: { domain: string; ips: string[] }) {
  const rows = recordRows(domain, ips)
  if (rows.length === 0) {
    return (
      <p className="notice">
        Choose this server's addresses first. Then you'll see the DNS records to create for {domain}.
      </p>
    )
  }
  return (
    <div className="dns-records">
      <p>
        Create these records where you manage the DNS of your domain. Use an A record for each IPv4 address and an AAAA
        record for each IPv6 address:
      </p>
      <ul className="dns-record-list" aria-label={`DNS records for ${domain}`}>
        {rows.map((row) => (
          <li key={`${row.type}:${row.value}`} className="dns-record">
            <span className="dns-record-type">{row.type} record</span>
            <dl>
              <div>
                <dt>Name</dt>
                <dd>
                  <code>{row.name}</code>
                  <CopyButton text={row.name} what={`the name of the ${row.type} record`} />
                </dd>
              </div>
              <div>
                <dt>Value</dt>
                <dd>
                  <code>{row.value}</code>
                  <CopyButton text={row.value} what={`the value ${row.value}`} />
                </dd>
              </div>
            </dl>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function PanelStatus({ domain, ips }: { domain: string; ips: string[] }) {
  const checks = usePanelChecks(domain ? `${domain}|${ips.join(',')}` : '')
  const current = checks.status === 'ready' || checks.status === 'error' ? checks.checks : null

  return (
    <div className="panel-status">
      <h3>Status</h3>
      <dl className="panel-status-list" aria-live="polite">
        <div>
          <dt>DNS</dt>
          <dd>
            {current ? (
              <StatusBadge tone={dnsTone(current.dns)}>{dnsText(current.dns)}</StatusBadge>
            ) : (
              <span className="muted">Checking…</span>
            )}
          </dd>
        </div>
        <div>
          <dt>Certificate</dt>
          <dd>
            {current ? (
              <StatusBadge tone={certificateTone(current.certificate)}>
                {certificateText(current.certificate)}
              </StatusBadge>
            ) : (
              <span className="muted">Checking…</span>
            )}
          </dd>
        </div>
      </dl>
      {checks.status === 'error' ? (
        <p className="muted">Couldn't check right now. {errorMessage(checks.error)} Trying again in a few seconds.</p>
      ) : null}
      <p className="muted">
        New DNS records can take a few minutes to be visible.
        {current?.certificate.state === 'not_applicable'
          ? ''
          : ' The certificate is requested automatically once the domain points to this server.'}{' '}
        This status updates by itself.
      </p>
    </div>
  )
}

type FormProps = {
  settings: PanelSettings
  onSaved: (settings: PanelSettings) => void
  onDirtyChange?: (dirty: boolean) => void
  askBeforeChange?: boolean
  allowEmpty?: boolean
  /** Shows the form for a dialog: Cancel and Save below the field, and onDone after saving. */
  onDone?: () => void
}

export function PanelDomainForm({ settings, onSaved, onDirtyChange, askBeforeChange, allowEmpty, onDone }: FormProps) {
  const [draft, setDraft] = useState(settings.panel_domain)
  const [error, setError] = useState<string | null>(null)

  if (settings.panel_domain_locked) return <LockedField label="Panel domain" value={settings.panel_domain} />

  function edit(value: string) {
    setDraft(value)
    setError(null)
    onDirtyChange?.(normalizeDomain(value) !== settings.panel_domain)
  }

  async function save() {
    const domain = normalizeDomain(draft)
    const problem = domain || !allowEmpty ? validateDomain(domain) : null
    if (problem) {
      setError(problem)
      return
    }
    if (domain === settings.panel_domain) {
      setDraft(domain)
      onDirtyChange?.(false)
      onDone?.()
      return
    }
    if (askBeforeChange && settings.panel_domain) {
      const yes = await confirm({ ...domainChangeQuestion(settings.panel_domain, domain), cancelLabel: 'Cancel' })
      if (!yes) return
    }
    try {
      const result = await saveSettings({ panel_domain: domain })
      setDraft(result.settings.panel_domain)
      onDirtyChange?.(false)
      onSaved(result.settings)
      announceSaved(result, domain ? `The panel's address is now ${result.settings.panel_domain}.` : 'Removed the panel domain.')
      onDone?.()
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  const input = (
    <Input
      value={draft}
      placeholder="panel.example.com"
      autoComplete="off"
      autoCapitalize="none"
      spellCheck={false}
      onValueChange={edit}
    />
  )

  return (
    <form className={onDone ? 'form' : 'inline-form'} onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field invalid={error ? true : undefined}>
        <FieldLabel>Panel domain</FieldLabel>
        {onDone ? (
          input
        ) : (
          <div className="inline-control">
            {input}
            <Button type="submit" pendingAnnouncement="Saving" onClick={save}>
              Save domain
            </Button>
          </div>
        )}
        <FieldDescription>
          Use a subdomain of a domain you own, for example panel.example.com.
          {allowEmpty ? ' Leave it empty to reach the panel by IP address.' : ''}
        </FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      {onDone ? (
        <div className="form-actions">
          <DialogClose>Cancel</DialogClose>
          <Button type="submit" data-variant="solid" pendingAnnouncement="Saving" onClick={save}>
            Save domain
          </Button>
        </div>
      ) : null}
    </form>
  )
}

export function PanelAddressDetails({ settings }: { settings: PanelSettings }) {
  if (!settings.panel_domain) return null
  return (
    <>
      <DnsRecords domain={settings.panel_domain} ips={settings.server_ips} />
      <PanelStatus domain={settings.panel_domain} ips={settings.server_ips} />
    </>
  )
}
