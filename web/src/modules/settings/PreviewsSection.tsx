import { Button } from '@ui-sdk/react/button'
import { confirm } from '@ui-sdk/react/alert-dialog'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { toast } from '@ui-sdk/react/toast'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { pointedMessage } from '../../shared/domains/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import {
  announceSaved,
  DnsRecords,
  getPanelChecks,
  normalizeDomain,
  saveSettings,
  type PanelSettings,
  type PreviewDnsCheck,
} from '../../shared/settings/index.ts'
import { FormDialog, Section, StatusBadge } from '../../shared/ui/index.ts'
import { pointPreviewDomain, suggestedPreviewDomain, validatePreviewDomain } from './previews.ts'

type FormProps = { settings: PanelSettings; onSaved: (settings: PanelSettings) => void }

const SAFER =
  'A domain of its own, not one below the panel\'s address, keeps the files of the sites further away from the panel.'

function PreviewDomainForm({ settings, onSaved, onDone }: FormProps & { onDone: () => void }) {
  const [draft, setDraft] = useState(settings.preview_domain)
  const [error, setError] = useState<string | null>(null)
  const suggestion = suggestedPreviewDomain(settings)

  async function save() {
    const domain = normalizeDomain(draft)
    const problem = validatePreviewDomain(domain, settings)
    if (problem) {
      setError(problem)
      return
    }
    if (domain === settings.preview_domain) {
      onDone()
      return
    }
    if (settings.preview_domain) {
      const yes = await confirm({
        title: domain ? `Move previews to ${domain}?` : 'Turn preview addresses off?',
        description: domain
          ? 'Preview links that were shared so far stop working; every version gets a new link.'
          : 'Preview links stop working. Versions can still be published without making them live.',
        confirmLabel: domain ? 'Move previews' : 'Turn off',
        cancelLabel: 'Cancel',
      })
      if (!yes) return
    }
    try {
      const result = await saveSettings({ preview_domain: domain })
      onSaved(result.settings)
      announceSaved(result, domain ? `Versions now open below ${domain}.` : 'Versions have no preview addresses anymore.')
      onDone()
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <form className="form" onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field invalid={error ? true : undefined}>
        <FieldLabel>Domain for previews</FieldLabel>
        <Input
          value={draft}
          placeholder={suggestion || 'preview.example.com'}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          autoFocus
          onValueChange={(value) => {
            setDraft(value)
            setError(null)
          }}
        />
        <FieldDescription>
          Every version gets an address below it. {SAFER} Leave it empty to turn preview addresses off.
        </FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      {suggestion && !draft ? (
        <div>
          <Button onClick={() => setDraft(suggestion)}>Use {suggestion}</Button>
        </div>
      ) : null}
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" pendingAnnouncement="Saving" onClick={save}>
          Save domain
        </Button>
      </div>
    </form>
  )
}

function dnsBadge(check: PreviewDnsCheck): { text: string; tone: 'positive' | 'neutral' | 'warning' } {
  switch (check.state) {
    case 'points_here':
      return { text: 'Points to this server', tone: 'positive' }
    case 'points_elsewhere':
      return { text: 'Points somewhere else', tone: 'warning' }
    case 'no_record':
      return { text: 'No record yet', tone: 'warning' }
    default:
      return { text: "Couldn't be checked right now", tone: 'neutral' }
  }
}

function PreviewDns({ settings }: { settings: PanelSettings }) {
  const [round, setRound] = useState(0)
  const checks = useResource(getPanelChecks, `preview-dns:${settings.preview_domain}:${settings.server_ips.join(',')}:${round}`)

  if (checks.status === 'loading') return <p className="muted">Checking where the preview addresses point…</p>
  if (checks.status === 'error') return <p className="muted">Couldn't check where the preview addresses point: {errorMessage(checks.error)}</p>
  const check = checks.data.preview_dns
  if (!check || !check.name) return null
  const badge = dnsBadge(check)

  async function point() {
    try {
      const changed = await pointPreviewDomain()
      toast.success(pointedMessage(check!.name, changed, check!.zone?.zone))
    } catch (error) {
      toast.error(errorMessage(error))
    }
    setRound((n) => n + 1)
  }

  return (
    <div className="settings-stack">
      <div className="preview-dns">
        <span>
          All names below <code>{settings.preview_domain}</code>
        </span>
        <StatusBadge tone={badge.tone}>{badge.text}</StatusBadge>
        {check.state === 'points_elsewhere' && check.found.length > 0 ? (
          <span className="muted">Right now they point to {check.found.join(', ')}.</span>
        ) : null}
      </div>
      {check.state === 'points_here' ? null : check.zone ? (
        <div>
          <p className="muted">
            {settings.preview_domain} is in {check.zone.zone} of your account {check.zone.connection_name}.
          </p>
          <Button pendingAnnouncement="Pointing" onClick={point}>
            Point to this server
          </Button>
        </div>
      ) : (
        <DnsRecords domain={check.name} ips={settings.server_ips} />
      )}
    </div>
  )
}

export function PreviewsSection({ settings, onSaved }: FormProps) {
  const [open, setOpen] = useState(false)
  return (
    <Section
      title="Previews"
      description="Every version of a site can be opened on an address of its own, also before it is made live. Anyone with such a link can open that version."
      actions={
        <Button onClick={() => setOpen(true)}>
          Change<span className="visually-hidden"> the domain for previews</span>
        </Button>
      }
    >
      <div className="settings-stack">
        <div className="setting-current">
          <span className="visually-hidden">Domain for previews: </span>
          {settings.preview_domain ? (
            <p className="setting-value">
              <code>{settings.preview_domain}</code>
            </p>
          ) : (
            <p className="muted">
              Off. Versions can still be published without making them live, but they have no address to open them
              before. Set a domain to turn previews on.
            </p>
          )}
        </div>
        {settings.preview_domain ? <PreviewDns settings={settings} /> : null}
      </div>
      <FormDialog
        open={open}
        onOpenChange={setOpen}
        title="Domain for previews"
        description="Choose a domain whose names all point to this server. Each version of a site opens on a name below it."
      >
        <PreviewDomainForm settings={settings} onSaved={onSaved} onDone={() => setOpen(false)} />
      </FormDialog>
    </Section>
  )
}
