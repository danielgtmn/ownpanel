import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../api/index.ts'
import type { PanelSettings } from './api.ts'
import { LockedField } from './LockedField.tsx'
import { validateEmail } from './rules.ts'
import { announceSaved, saveSettings } from './save.ts'

export const CERTIFICATE_EMAIL_LABEL = 'E-mail for certificate notices'
const LABEL = CERTIFICATE_EMAIL_LABEL

type Props = {
  settings: PanelSettings
  onSaved: (settings: PanelSettings) => void
  /** Shows the form for a dialog: Cancel and Save below the field, and onDone after saving. */
  onDone?: () => void
}

export function CertificateEmailForm({ settings, onSaved, onDone }: Props) {
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
    if (email === settings.acme_email) {
      onDone?.()
      return
    }
    try {
      const result = await saveSettings({ acme_email: email })
      setDraft(result.settings.acme_email)
      onSaved(result.settings)
      announceSaved(result, email ? 'Saved the e-mail for certificate notices.' : 'Removed the e-mail for certificate notices.')
      onDone?.()
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  const input = (
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
  )

  return (
    <form className={onDone ? 'form' : 'inline-form'} onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field invalid={error ? true : undefined}>
        <FieldLabel>{LABEL}</FieldLabel>
        {onDone ? (
          input
        ) : (
          <div className="inline-control">
            {input}
            <Button type="submit" pendingAnnouncement="Saving" onClick={save}>
              Save e-mail
            </Button>
          </div>
        )}
        <FieldDescription>
          Optional. The service that issues the certificates may write to this address about them, for example before one expires.
        </FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      {onDone ? (
        <div className="form-actions">
          <DialogClose>Cancel</DialogClose>
          <Button type="submit" data-variant="solid" pendingAnnouncement="Saving" onClick={save}>
            Save e-mail
          </Button>
        </div>
      ) : null}
    </form>
  )
}
