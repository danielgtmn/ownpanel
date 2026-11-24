import { Button } from '@ui-sdk/react/button'
import { Field, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { usePageTitle } from '../shared/hooks/index.ts'
import { signIn, signInErrorMessage } from '../shared/session/index.ts'
import { PasswordField } from '../shared/ui/index.ts'

export function SignInScreen({ notice }: { notice: string | null }) {
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
      setError(!trimmed ? 'Enter your name.' : 'Enter your password.')
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
          <FieldLabel>Name</FieldLabel>
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
      </form>
    </div>
  )
}
