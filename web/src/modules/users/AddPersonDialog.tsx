import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { PASSWORD_MIN, type Role } from '../../shared/session/index.ts'
import { FormDialog, PasswordField } from '../../shared/ui/index.ts'
import { EMAIL_HINT, validateNewUser, type NewUserErrors } from './rules.ts'

type NewPerson = { name: string; email: string; password: string; role: Role }

type FormErrors = NewUserErrors & { form?: string }

function AddPersonForm({ onSubmit }: { onSubmit: (person: NewPerson) => Promise<void> }) {
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [errors, setErrors] = useState<FormErrors>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateNewUser(name, password, email)
    if (found.name || found.email || found.password) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit({ name: name.trim(), email: email.trim(), password, role })
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Name</FieldLabel>
        <Input
          value={name}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          autoFocus
          onValueChange={(value) => {
            setName(value)
            setErrors((current) => ({ ...current, name: undefined, form: undefined }))
          }}
        />
        <FieldDescription>3 to 32 lowercase letters, numbers, dots, underscores or hyphens.</FieldDescription>
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>
      <Field name="email" invalid={errors.email ? true : undefined}>
        <FieldLabel>E-mail address (optional)</FieldLabel>
        <Input
          type="email"
          value={email}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          onValueChange={(value) => {
            setEmail(value)
            setErrors((current) => ({ ...current, email: undefined, form: undefined }))
          }}
        />
        <FieldDescription>{EMAIL_HINT}</FieldDescription>
        {errors.email ? <FieldError match>{errors.email}</FieldError> : null}
      </Field>
      <PasswordField
        name="password"
        label="Initial password"
        autoComplete="new-password"
        value={password}
        description={`At least ${PASSWORD_MIN} characters. Share it with the person; they can change it after signing in.`}
        error={errors.password}
        onValueChange={(value) => {
          setPassword(value)
          setErrors((current) => ({ ...current, password: undefined, form: undefined }))
        }}
      />
      <Field>
        <FieldLabel>Role</FieldLabel>
        <RadioGroup value={role} onValueChange={(value) => value && setRole(value as Role)}>
          <Field>
            <Radio value="member">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Member</FieldLabel>
              <FieldDescription>Creates and manages their own sites and access tokens.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="admin">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Administrator</FieldLabel>
              <FieldDescription>Also sees every site, manages people and reads the activity.</FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Adding…' : 'Add person'}
        </Button>
      </div>
    </form>
  )
}

type AddPersonDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (person: NewPerson) => Promise<void>
}

export function AddPersonDialog({ open, onOpenChange, onSubmit }: AddPersonDialogProps) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Add person"
      description="Give someone their own account to sign in to this panel."
    >
      <AddPersonForm onSubmit={onSubmit} />
    </FormDialog>
  )
}
