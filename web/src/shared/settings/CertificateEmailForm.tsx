import { Button } from '@ui-sdk/react/button'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../api/index.ts'
import type { PanelSettings } from './api.ts'
import { LockedField } from './LockedField.tsx'
import { validateEmail } from './rules.ts'
import { announceSaved, saveSettings } from './save.ts'

const LABEL = 'E-mail for certificate notices'

type Props = {
  settings: PanelSettings
  onSaved: (settings: PanelSettings) => void
}

export function CertificateEmailForm({ settings, onSaved }: Props) {
  const [draft, setDraft] = useState(settings.acme_email)
  const [error, setError] = useState<string | null>(null)

  if (settings.acme_email_locked) return <LockedField label={LABEL} value={settings.acme_email} />

  async function save() {
    const email = draft.trim()
    const problem = validateEmail(email)
    if (problem) {
      setError(problem)
      return
    }
    if (email === settings.acme_email) return
    try {
      const result = await saveSettings({ acme_email: email })
      setDraft(result.settings.acme_email)
      onSaved(result.settings)
      announceSaved(result, email ? 'Saved the e-mail for certificate notices.' : 'Removed the e-mail for certificate notices.')
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <form className="inline-form" onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field invalid={error ? true : undefined}>
        <FieldLabel>{LABEL}</FieldLabel>
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
            Save e-mail
          </Button>
        </div>
        <FieldDescription>
          Optional. The service that issues the certificates may write to this address about them, for example before one expires.
        </FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
    </form>
  )
}
