import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import type { User } from '../../shared/session/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import { EMAIL_HINT, validateOptionalEmail } from './rules.ts'

function PersonEmailForm({ person, onSubmit }: { person: User; onSubmit: (email: string) => Promise<void> }) {
  const [email, setEmail] = useState(person.email)
  const [error, setError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function save(next: string) {
    setBusy(true)
    setFormError(null)
    try {
      await onSubmit(next)
    } catch (caught) {
      setFormError(errorMessage(caught))
      setBusy(false)
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const trimmed = email.trim()
    const invalid = validateOptionalEmail(trimmed)
    if (invalid) {
      setError(invalid)
      return
    }
    await save(trimmed)
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="email" invalid={error ? true : undefined}>
        <FieldLabel>E-mail address</FieldLabel>
        <Input
          type="email"
          value={email}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          autoFocus
          onValueChange={(value) => {
            setEmail(value)
            setError(null)
            setFormError(null)
          }}
        />
        <FieldDescription>{EMAIL_HINT} Leave it empty to remove the address.</FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      {formError ? (
        <p className="form-error" role="alert">
          {formError}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

type PersonEmailDialogProps = {
  person: User | null
  onClose: () => void
  onSubmit: (email: string) => Promise<void>
}

export function PersonEmailDialog({ person, onClose, onSubmit }: PersonEmailDialogProps) {
  return (
    <FormDialog
      open={person !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title="E-mail address"
      description={person ? `The address ${person.name} can sign in with and get a link to choose a new password.` : ''}
    >
      {person ? <PersonEmailForm person={person} onSubmit={onSubmit} /> : null}
    </FormDialog>
  )
}
