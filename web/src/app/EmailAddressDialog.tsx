import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { toast } from '@ui-sdk/react/toast'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../shared/api/index.ts'
import { setOwnEmail, useSession, validateEmail } from '../shared/session/index.ts'
import { FormDialog, PasswordField } from '../shared/ui/index.ts'

const EMAIL_HINT = 'Used to send you a link when you forget your password. You can also sign in with it.'

function EmailAddressForm({ onDone }: { onDone: () => void }) {
  const { user } = useSession()
  const [email, setEmail] = useState(user.email)
  const [current, setCurrent] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [currentError, setCurrentError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function save(next: string) {
    if (!current) {
      setCurrentError('Enter your current password.')
      return
    }
    setBusy(true)
    setFormError(null)
    try {
      await setOwnEmail(next, current)
      toast.success(next ? 'Your e-mail address was saved.' : 'Your e-mail address was removed.')
      onDone()
    } catch (caught) {
      setFormError(errorMessage(caught))
      setBusy(false)
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const trimmed = email.trim()
    if (!trimmed && !user.email) {
      setError('Enter an e-mail address.')
      return
    }
    const invalid = trimmed ? validateEmail(trimmed) : null
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
          autoComplete="email"
          autoCapitalize="none"
          spellCheck={false}
          autoFocus
          onValueChange={(value) => {
            setEmail(value)
            setError(null)
            setFormError(null)
          }}
        />
        <FieldDescription>{EMAIL_HINT}</FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      <PasswordField
        name="current"
        label="Current password"
        autoComplete="current-password"
        value={current}
        description="Needed so nobody else can change where links for a new password are sent."
        error={currentError ?? undefined}
        onValueChange={(value) => {
          setCurrent(value)
          setCurrentError(null)
          setFormError(null)
        }}
      />
      {formError ? (
        <p className="form-error" role="alert">
          {formError}
        </p>
      ) : null}
      <div className="form-actions">
        {user.email ? (
          <Button data-variant="destructive" disabled={busy} onClick={() => void save('')}>
            Remove address
          </Button>
        ) : null}
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

type EmailAddressDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function EmailAddressDialog({ open, onOpenChange }: EmailAddressDialogProps) {
  const { user } = useSession()
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="E-mail address"
      description={
        user.email
          ? `Your account uses ${user.email}.`
          : "Your account has no e-mail address yet. Without one, an administrator has to set a new password if you forget yours."
      }
    >
      <EmailAddressForm onDone={() => onOpenChange(false)} />
    </FormDialog>
  )
}
