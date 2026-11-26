import { Button } from '@ui-sdk/react/button'
import { Checkbox, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { PASSWORD_MIN } from '../shared/session/validation.ts'
import { copyText, FormDialog, PasswordField } from '../shared/ui/index.ts'
import { accountErrorMessage, isSessionEnded } from './api.ts'
import { NAME_MAX, passwordErrors, validateAppPasswordName, validateDisplayName, type PasswordErrors } from './rules.ts'

function FormError({ message }: { message?: string }) {
  return message ? (
    <p className="form-error" role="alert">
      {message}
    </p>
  ) : null
}

// Runs a submit; a failure shows in the form unless the session ended (the screen changes then).
async function attempt(run: () => Promise<void>, fail: (message: string) => void) {
  try {
    await run()
  } catch (error) {
    if (!isSessionEnded(error)) fail(accountErrorMessage(error))
  }
}

// ---- display name ----

function DisplayNameForm({ current, onSubmit }: { current: string; onSubmit: (name: string) => Promise<void> }) {
  const [name, setName] = useState(current)
  const [errors, setErrors] = useState<{ name?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateDisplayName(name)
    if (found) {
      setErrors({ name: found })
      return
    }
    setErrors({})
    setBusy(true)
    await attempt(
      () => onSubmit(name.trim()),
      (form) => {
        setErrors({ form })
        setBusy(false)
      },
    )
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="display_name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Your name</FieldLabel>
        <Input
          value={name}
          placeholder="Anna Example"
          maxLength={NAME_MAX}
          autoComplete="name"
          autoFocus
          onValueChange={(value) => {
            setName(value)
            setErrors({})
          }}
        />
        <FieldDescription>Leave it empty to send without a name.</FieldDescription>
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Save name'}
        </Button>
      </div>
    </form>
  )
}

export function DisplayNameDialog({
  open,
  current,
  onOpenChange,
  onSubmit,
}: {
  open: boolean
  current: string
  onOpenChange: (open: boolean) => void
  onSubmit: (name: string) => Promise<void>
}) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Change your name"
      description="The name people see with the mail you send. Mail programs that set a name of their own keep using theirs."
    >
      <DisplayNameForm current={current} onSubmit={onSubmit} />
    </FormDialog>
  )
}

// ---- password ----

export type PasswordChange = { current: string; password: string; revokeAppPasswords: boolean }

export function ChangePasswordForm({
  hasAppPasswords,
  onSubmit,
}: {
  hasAppPasswords: boolean
  onSubmit: (change: PasswordChange) => Promise<void>
}) {
  const [current, setCurrent] = useState('')
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [revoke, setRevoke] = useState(false)
  const [errors, setErrors] = useState<PasswordErrors & { form?: string }>({})
  const [busy, setBusy] = useState(false)

  function clear(field: keyof PasswordErrors) {
    setErrors((existing) => ({ ...existing, [field]: undefined, form: undefined }))
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = passwordErrors(current, password, repeat)
    if (found.current || found.password || found.repeat) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    await attempt(
      () => onSubmit({ current, password, revokeAppPasswords: revoke }),
      (form) => {
        setErrors({ form })
        setBusy(false)
      },
    )
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
      {hasAppPasswords ? (
        <Field className="account-choice">
          <Checkbox checked={revoke} disabled={busy} onCheckedChange={(next) => setRevoke(next === true)}>
            <CheckboxIndicator />
          </Checkbox>
          <div className="choice-text">
            <FieldLabel>Also sign out mail programs that use app passwords</FieldLabel>
            <FieldDescription>
              All your app passwords stop working. Use this if a device was lost or someone else may know one.
            </FieldDescription>
          </div>
        </Field>
      ) : null}
      <p className="muted account-dialog-note">
        Mail programs and phones that use your mailbox password need the new one. You stay signed in here and are
        signed out of this page in other browsers.
      </p>
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Change password'}
        </Button>
      </div>
    </form>
  )
}

export function ChangePasswordDialog({
  open,
  hasAppPasswords,
  onOpenChange,
  onSubmit,
}: {
  open: boolean
  hasAppPasswords: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (change: PasswordChange) => Promise<void>
}) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Change password"
      description="Choose a new password for your mailbox."
    >
      <ChangePasswordForm hasAppPasswords={hasAppPasswords} onSubmit={onSubmit} />
    </FormDialog>
  )
}

// ---- app passwords ----

function AppPasswordForm({ onSubmit }: { onSubmit: (name: string) => Promise<void> }) {
  const [name, setName] = useState('')
  const [errors, setErrors] = useState<{ name?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateAppPasswordName(name)
    if (found) {
      setErrors({ name: found })
      return
    }
    setErrors({})
    setBusy(true)
    await attempt(
      () => onSubmit(name.trim()),
      (form) => {
        setErrors({ form })
        setBusy(false)
      },
    )
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Device or program</FieldLabel>
        <Input
          value={name}
          placeholder="Phone"
          maxLength={NAME_MAX}
          autoComplete="off"
          autoFocus
          onValueChange={(value) => {
            setName(value)
            setErrors({})
          }}
        />
        <FieldDescription>Where you will use it, so you can tell your app passwords apart later.</FieldDescription>
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Creating…' : 'Create app password'}
        </Button>
      </div>
    </form>
  )
}

export function CreateAppPasswordDialog({
  open,
  onOpenChange,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (name: string) => Promise<void>
}) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Create app password"
      description="A password of its own for one phone or mail program. If the device is lost, revoke just this one."
    >
      <AppPasswordForm onSubmit={onSubmit} />
    </FormDialog>
  )
}

export type CreatedAppPassword = { address: string; name: string; password: string }

// Shows a new app password once. It lives only in the caller's state, which drops it when the dialog closes.
export function NewAppPasswordDialog({ created, onClose }: { created: CreatedAppPassword | null; onClose: () => void }) {
  return (
    <Dialog
      open={created !== null}
      onOpenChange={(next, details) => {
        if (next) return
        if (details.reason === 'outside-press') {
          details.cancel()
          return
        }
        onClose()
      }}
    >
      <DialogBackdrop />
      <DialogContent>
        <div className="dialog-head">
          <DialogTitle>Your new app password</DialogTitle>
          <DialogDescription>
            Enter it in “{created?.name}” as the password for {created?.address}. Copy it now: it won't be shown again.
            If you lose it, revoke it and create a new one.
          </DialogDescription>
        </div>
        <div className="form">
          <Field name="app_password">
            <FieldLabel>App password</FieldLabel>
            <Input
              readOnly
              value={created?.password ?? ''}
              spellCheck={false}
              autoComplete="off"
              className="account-app-password"
              onFocus={(event) => event.currentTarget.select()}
            />
          </Field>
          <div className="form-actions">
            <Button onClick={() => (created ? copyText(created.password) : undefined)}>Copy password</Button>
            <DialogClose data-variant="solid">I’ve entered it</DialogClose>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
