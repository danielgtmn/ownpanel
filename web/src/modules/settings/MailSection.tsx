import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage, isNotFound } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import {
  announceSaved,
  LockedField,
  normalizeDomain,
  saveSettings,
  validateEmail,
  type PanelSettings,
} from '../../shared/settings/index.ts'
import { Section, StatusBadge } from '../../shared/ui/index.ts'
import { getMailHealth, healthLine, validateMailHost } from './mailHealth.ts'

type FormProps = { settings: PanelSettings; onSaved: (settings: PanelSettings) => void }

const HOST_LABEL = 'Name of the mail server'

function MailHostForm({ settings, onSaved }: FormProps) {
  const [draft, setDraft] = useState(settings.mail_host)
  const [error, setError] = useState<string | null>(null)

  if (settings.mail_host_locked) return <LockedField label={HOST_LABEL} value={settings.mail_host} />

  async function save() {
    const host = normalizeDomain(draft)
    const problem = validateMailHost(host)
    if (problem) {
      setError(problem)
      return
    }
    if (host === settings.mail_host) return
    if (settings.mail_host) {
      const yes = await confirm({
        title: host ? `Rename the mail server to ${host}?` : 'Remove the name of the mail server?',
        description: host
          ? 'Mail programs and the mail records of your domains have to use the new name. Mail for domains whose records still name the old one may not arrive.'
          : 'Without a name, no domain starts receiving mail here.',
        confirmLabel: host ? 'Rename' : 'Remove name',
        cancelLabel: 'Cancel',
      })
      if (!yes) return
    }
    try {
      const result = await saveSettings({ mail_host: host })
      setDraft(result.settings.mail_host)
      onSaved(result.settings)
      announceSaved(result, host ? `The mail server is now called ${host}.` : 'Removed the name of the mail server.')
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <form className="inline-form" onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field invalid={error ? true : undefined}>
        <FieldLabel>{HOST_LABEL}</FieldLabel>
        <div className="inline-control">
          <Input
            value={draft}
            placeholder="mail.example.com"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            onValueChange={(value) => {
              setDraft(value)
              setError(null)
            }}
          />
          <Button type="submit" pendingAnnouncement="Saving" onClick={save}>
            Save name
          </Button>
        </div>
        <FieldDescription>
          Mail programs connect to this name, and the mail records of your domains point to it. Use a name of its own,
          like mail.example.com, that points to this server.
        </FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
    </form>
  )
}

function PostmasterForm({ settings, onSaved }: FormProps) {
  const [draft, setDraft] = useState(settings.postmaster_address)
  const [error, setError] = useState<string | null>(null)

  async function save() {
    const address = draft.trim().toLowerCase()
    const problem = validateEmail(address)
    if (problem) {
      setError(problem)
      return
    }
    if (address === settings.postmaster_address) return
    try {
      const result = await saveSettings({ postmaster_address: address })
      setDraft(result.settings.postmaster_address)
      onSaved(result.settings)
      announceSaved(result, address ? `Reports about mail problems go to ${address}.` : 'Removed the postmaster address.')
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <form className="inline-form" onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field invalid={error ? true : undefined}>
        <FieldLabel>Postmaster address</FieldLabel>
        <div className="inline-control">
          <Input
            type="email"
            value={draft}
            placeholder="you@example.com"
            autoComplete="email"
            onValueChange={(value) => {
              setDraft(value)
              setError(null)
            }}
          />
          <Button type="submit" pendingAnnouncement="Saving" onClick={save}>
            Save address
          </Button>
        </div>
        <FieldDescription>
          Receives mail to postmaster@ and abuse@ of the mail server, where other mail servers send reports about
          problems.
        </FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
    </form>
  )
}

function MailHealthList({ version }: { version: string }) {
  const health = useResource(getMailHealth, `mail-health:${version}`)
  if (health.status === 'loading') return <p className="muted">Checking the mail server…</p>
  if (health.status === 'error') {
    if (isNotFound(health.error)) return null
    return <p className="muted">Couldn't check the mail server. {errorMessage(health.error)}</p>
  }
  const host = health.data.mail_host
  return (
    <div className="mail-health">
      <div className="mail-health-head">
        <h3>How the mail server is doing</h3>
        <Button pendingAnnouncement="Checking" onClick={() => health.reload()}>
          Check again
        </Button>
      </div>
      <ul className="mail-health-list" aria-label="How the mail server is doing">
        {health.data.checks.map((check) => {
          const line = healthLine(check, host)
          return (
            <li key={check.id} className="mail-health-item">
              <div className="mail-health-row">
                <span className="mail-health-title">{line.title}</span>
                <StatusBadge tone={line.tone}>{line.badge}</StatusBadge>
              </div>
              {line.hint ? <p className="muted">{line.hint}</p> : null}
              {check.detail && check.state !== 'ok' ? <p className="muted">{check.detail}</p> : null}
            </li>
          )
        })}
      </ul>
    </div>
  )
}

export function MailSection({ settings, onSaved }: FormProps) {
  return (
    <Section
      title="E-mail"
      description="The mail server that receives and sends the mail of the domains with e-mail turned on."
    >
      <div className="settings-stack">
        <MailHostForm settings={settings} onSaved={onSaved} />
        <PostmasterForm settings={settings} onSaved={onSaved} />
        <MailHealthList version={`${settings.mail_host}|${settings.postmaster_address}`} />
      </div>
    </Section>
  )
}
