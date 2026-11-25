import { Button } from '@ui-sdk/react/button'
import { Field, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../shared/api/index.ts'
import { usePageTitle } from '../shared/hooks/index.ts'
import { hrefFor } from '../shared/routing/index.ts'
import { requestPasswordReset, signIn, signInErrorMessage, validateEmail } from '../shared/session/index.ts'
import { PasswordField } from '../shared/ui/index.ts'
import { FORGOT_PATH } from './screen.ts'

type SignInScreenProps = {
  notice: string | null
  passwordReset: boolean
  forgot: boolean
}

export function SignInScreen({ notice, passwordReset, forgot }: SignInScreenProps) {
  return forgot ? <ForgotPasswordForm /> : <SignInForm notice={notice} passwordReset={passwordReset} />
}

function SignInForm({ notice, passwordReset }: { notice: string | null; passwordReset: boolean }) {
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [missingName, setMissingName] = useState(false)
  const [busy, setBusy] = useState(false)
  usePageTitle('Sign in')

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const trimmed = name.trim()
    if (!trimmed || !password) {
      setMissingName(!trimmed)
      setError(!trimmed ? 'Enter your name or e-mail address.' : 'Enter your password.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await signIn(trimmed, password)
    } catch (caught) {
      setError(signInErrorMessage(caught))
      setBusy(false)
    }
  }

  return (
    <div className="gate">
      <form className="gate-card" onSubmit={submit} noValidate>
        <div className="gate-head">
          <span className="brand">ownpanel</span>
          <h1>Sign in</h1>
          {notice ? (
            <p className="notice" role="status">
              {notice}
            </p>
          ) : null}
        </div>
        <Field name="name" invalid={missingName ? true : undefined}>
          <FieldLabel>Name or e-mail address</FieldLabel>
          <Input
            value={name}
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            autoFocus
            onValueChange={(value) => {
              setName(value)
              setMissingName(false)
              setError(null)
            }}
          />
          {missingName && error ? <FieldError match>{error}</FieldError> : null}
        </Field>
        <PasswordField
          name="password"
          label="Password"
          autoComplete="current-password"
          value={password}
          error={!missingName ? error : null}
          onValueChange={(value) => {
            setPassword(value)
            setError(null)
          }}
        />
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
        {passwordReset ? (
          <p className="gate-foot">
            <a href={hrefFor(FORGOT_PATH)}>Forgot your password?</a>
          </p>
        ) : null}
      </form>
    </div>
  )
}

function ForgotPasswordForm() {
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState(false)
  usePageTitle('Forgot your password?')

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const invalid = validateEmail(email)
    if (invalid) {
      setError(invalid)
      return
    }
    setBusy(true)
    setError(null)
    try {
      await requestPasswordReset(email.trim())
      setSent(true)
    } catch (caught) {
      setError(errorMessage(caught))
    }
    setBusy(false)
  }

  if (sent) {
    return (
      <div className="gate">
        <div className="gate-card">
          <div className="gate-head">
            <span className="brand">ownpanel</span>
            <h1>Check your e-mail</h1>
            <p role="status">
              If this address belongs to an account, we sent a link to choose a new password. It works for one hour.
            </p>
          </div>
          <p className="gate-foot">
            <a href={hrefFor()}>Back to sign in</a>
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="gate">
      <form className="gate-card" onSubmit={submit} noValidate>
        <div className="gate-head">
          <span className="brand">ownpanel</span>
          <h1>Forgot your password?</h1>
          <p className="muted">Enter the e-mail address of your account. We'll send you a link to choose a new password.</p>
        </div>
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
            }}
          />
          {error ? <FieldError match>{error}</FieldError> : null}
        </Field>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Sending…' : 'Send link'}
        </Button>
        <p className="gate-foot">
          <a href={hrefFor()}>Back to sign in</a>
        </p>
      </form>
    </div>
  )
}
