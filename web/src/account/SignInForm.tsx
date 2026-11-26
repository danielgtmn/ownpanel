import { Button } from '@ui-sdk/react/button'
import { Field, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { PasswordField } from '../shared/ui/index.ts'
import { accountErrorMessage, signIn, type Me } from './api.ts'
import { signInErrors, type SignInErrors } from './rules.ts'

export function SignInForm({
  host,
  notice,
  onSignedIn,
}: {
  host: string
  notice: string | null
  onSignedIn: (me: Me) => void
}) {
  const [address, setAddress] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<SignInErrors>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = signInErrors(address, password)
    if (found.address || found.password) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      onSignedIn(await signIn(address.trim(), password))
    } catch (error) {
      setErrors({ form: accountErrorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <div className="gate">
      <form className="gate-card" onSubmit={submit} noValidate>
        <div className="gate-head">
          <span className="brand">{host}</span>
          <h1>Sign in to your mailbox</h1>
          <p className="muted">
            Change your password, create passwords for your phone and mail programs, and see how much room your mail
            takes.
          </p>
          {notice ? (
            <p className="notice" role="status">
              {notice}
            </p>
          ) : null}
        </div>
        <Field name="address" invalid={errors.address ? true : undefined}>
          <FieldLabel>E-mail address</FieldLabel>
          <Input
            type="email"
            value={address}
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            autoFocus
            onValueChange={(value) => {
              setAddress(value)
              setErrors({})
            }}
          />
          {errors.address ? <FieldError match>{errors.address}</FieldError> : null}
        </Field>
        <PasswordField
          name="password"
          label="Password"
          autoComplete="current-password"
          value={password}
          error={errors.password}
          onValueChange={(value) => {
            setPassword(value)
            setErrors({})
          }}
        />
        {errors.form ? (
          <p className="form-error" role="alert">
            {errors.form}
          </p>
        ) : null}
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
        <p className="muted account-gate-note">
          Use your mailbox password here; app passwords only work in mail programs. Forgot it? The people who manage
          this server can set a new one.
        </p>
      </form>
    </div>
  )
}
