import { Button } from '@ui-sdk/react/button'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { ApiError, errorMessage } from '../shared/api/index.ts'
import { usePageTitle } from '../shared/hooks/index.ts'
import {
  completeSetup,
  loadSession,
  PASSWORD_MIN,
  validatePasswordPair,
  validateUserName,
} from '../shared/session/index.ts'
import { PasswordField } from '../shared/ui/index.ts'

type Errors = { code?: string; name?: string; password?: string; repeat?: string; form?: string }

export function SetupScreen() {
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [errors, setErrors] = useState<Errors>({})
  const [busy, setBusy] = useState(false)
  usePageTitle('Set up')

  function clear(field: keyof Errors) {
    setErrors((current) => ({ ...current, [field]: undefined, form: undefined }))
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const next: Errors = {}
    if (!code.trim()) next.code = 'Enter the setup code.'
    const nameError = validateUserName(name)
    if (nameError) next.name = nameError
    Object.assign(next, validatePasswordPair(password, repeat))
    if (next.code || next.name || next.password || next.repeat) {
      setErrors(next)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await completeSetup(code.trim(), name.trim(), password)
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        await loadSession('This panel is already set up. Sign in to continue.')
        return
      }
      setErrors({ form: errorMessage(caught) })
      setBusy(false)
    }
  }

  return (
    <div className="gate">
      <form className="gate-card" onSubmit={submit} noValidate>
        <div className="gate-head">
          <span className="brand">ownpanel</span>
          <h1>Set up your panel</h1>
          <p className="muted">
            Whoever installed the panel will find a one-time setup code in a file on the server. The message shown when
            the panel started says where. Enter it here and create the first administrator account.
          </p>
        </div>
        <Field name="setup-code" invalid={errors.code ? true : undefined}>
          <FieldLabel>Setup code</FieldLabel>
          <Input
            value={code}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            autoFocus
            onValueChange={(value) => {
              setCode(value)
              clear('code')
            }}
          />
          {errors.code ? <FieldError match>{errors.code}</FieldError> : null}
        </Field>
        <Field name="name" invalid={errors.name ? true : undefined}>
          <FieldLabel>Your name</FieldLabel>
          <Input
            value={name}
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            onValueChange={(value) => {
              setName(value)
              clear('name')
            }}
          />
          <FieldDescription>3 to 32 lowercase letters, numbers, dots, underscores or hyphens.</FieldDescription>
          {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
        </Field>
        <PasswordField
          name="password"
          label="Password"
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
          label="Repeat password"
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
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Setting up…' : 'Create account and continue'}
        </Button>
      </form>
    </div>
  )
}
