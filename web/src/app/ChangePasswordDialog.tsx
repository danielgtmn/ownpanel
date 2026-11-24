import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { toast } from '@ui-sdk/react/toast'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../shared/api/index.ts'
import { changePassword, PASSWORD_MIN, validatePasswordPair } from '../shared/session/index.ts'
import { FormDialog, PasswordField } from '../shared/ui/index.ts'

type Errors = { current?: string; password?: string; repeat?: string; form?: string }

function ChangePasswordForm({ onDone }: { onDone: () => void }) {
  const [current, setCurrent] = useState('')
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [errors, setErrors] = useState<Errors>({})
  const [busy, setBusy] = useState(false)

  function clear(field: keyof Errors) {
    setErrors((existing) => ({ ...existing, [field]: undefined, form: undefined }))
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const next: Errors = { ...validatePasswordPair(password, repeat) }
    if (!current) next.current = 'Enter your current password.'
    if (next.current || next.password || next.repeat) {
      setErrors(next)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await changePassword(current, password)
      toast.success('Your password was changed.')
      onDone()
    } catch (caught) {
      setErrors({ form: errorMessage(caught) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <PasswordField
        name="current"
        label="Current password"
        autoComplete="current-password"
        value={current}
        error={errors.current}
        autoFocus
        onValueChange={(value) => {
          setCurrent(value)
          clear('current')
        }}
      />
      <PasswordField
        name="new"
        label="New password"
        autoComplete="new-password"
        value={password}
        description={`At least ${PASSWORD_MIN} characters.`}
        error={errors.password}
        onValueChange={(value) => {
          setPassword(value)
          clear('password')
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
          clear('repeat')
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
          {busy ? 'Saving…' : 'Change password'}
        </Button>
      </div>
    </form>
  )
}

type ChangePasswordDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ChangePasswordDialog({ open, onOpenChange }: ChangePasswordDialogProps) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Change password"
      description="Choose a new password for signing in."
    >
      <ChangePasswordForm onDone={() => onOpenChange(false)} />
    </FormDialog>
  )
}
