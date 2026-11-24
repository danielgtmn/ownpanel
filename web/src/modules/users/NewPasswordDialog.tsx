import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { PASSWORD_MIN, validatePasswordPair } from '../../shared/session/index.ts'
import { FormDialog, PasswordField } from '../../shared/ui/index.ts'

type Errors = { password?: string; repeat?: string; form?: string }

function NewPasswordForm({ onSubmit }: { onSubmit: (password: string) => Promise<void> }) {
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [errors, setErrors] = useState<Errors>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validatePasswordPair(password, repeat)
    if (found.password || found.repeat) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(password)
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <PasswordField
        name="password"
        label="New password"
        autoComplete="new-password"
        value={password}
        description={`At least ${PASSWORD_MIN} characters.`}
        error={errors.password}
        autoFocus
        onValueChange={(value) => {
          setPassword(value)
          setErrors((current) => ({ ...current, password: undefined, form: undefined }))
        }}
      />
      <PasswordField
        name="repeat"
        label="Repeat new password"
        autoComplete="new-password"
        value={repeat}
        error={errors.repeat}
        onValueChange={(value) => {
          setRepeat(value)
          setErrors((current) => ({ ...current, repeat: undefined, form: undefined }))
        }}
      />
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Set password'}
        </Button>
      </div>
    </form>
  )
}

type NewPasswordDialogProps = {
  name: string | null
  onClose: () => void
  onSubmit: (password: string) => Promise<void>
}

export function NewPasswordDialog({ name, onClose, onSubmit }: NewPasswordDialogProps) {
  return (
    <FormDialog
      open={name !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title="Set a new password"
      description={name ? `Choose a new password for ${name}. They will use it the next time they sign in.` : ''}
    >
      <NewPasswordForm onSubmit={onSubmit} />
    </FormDialog>
  )
}
