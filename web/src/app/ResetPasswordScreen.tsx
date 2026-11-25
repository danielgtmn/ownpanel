import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState, type FormEvent } from 'react'
import { ApiError, errorMessage } from '../shared/api/index.ts'
import { usePageTitle } from '../shared/hooks/index.ts'
import { hrefFor, replaceHash } from '../shared/routing/index.ts'
import {
  getSessionState,
  PASSWORD_MIN,
  PASSWORD_RESET_NOTICE,
  resetPassword,
  validatePasswordPair,
} from '../shared/session/index.ts'
import { PasswordField } from '../shared/ui/index.ts'
import { FORGOT_PATH } from './screen.ts'

type Errors = { password?: string; repeat?: string; form?: string }

type ResetPasswordScreenProps = {
  token: string
  // Signed out on a server that can send links: a broken link offers a new one.
  canAskAgain: boolean
  signedIn: boolean
}

const MISSING_LINK = 'This link is incomplete. Open it again from the e-mail, or ask for a new one.'

export function ResetPasswordScreen({ token, canAskAgain, signedIn }: ResetPasswordScreenProps) {
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [errors, setErrors] = useState<Errors>(token ? {} : { form: MISSING_LINK })
  const [linkFailed, setLinkFailed] = useState(!token)
  const [busy, setBusy] = useState(false)
  usePageTitle('Choose a new password')

  function clear(field: keyof Errors) {
    setErrors((existing) => ({ ...existing, [field]: undefined }))
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!token) return
    const found = validatePasswordPair(password, repeat)
    if (found.password || found.repeat) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await resetPassword(token, password)
    } catch (caught) {
      // A 400 is either the link or the password; only the server's message tells them apart.
      setLinkFailed(caught instanceof ApiError && caught.status === 400 && /link/i.test(caught.message))
      setErrors({ form: errorMessage(caught) })
      setBusy(false)
      return
    }
    // Someone else's link in a browser that stays signed in: the sign-in screen with its notice doesn't appear.
    if (getSessionState().status === 'signedIn') toast.success(PASSWORD_RESET_NOTICE)
    replaceHash('')
  }

  const back = signedIn ? 'Back to the panel' : 'Back to sign in'

  return (
    <div className="gate">
      <form className="gate-card" onSubmit={submit} noValidate>
        <div className="gate-head">
          <span className="brand">ownpanel</span>
          <h1>Choose a new password</h1>
          <p className="muted">You'll be signed out everywhere and can then sign in with the new password.</p>
        </div>
        {token ? (
          <>
            <PasswordField
              name="new"
              label="New password"
              autoComplete="new-password"
              value={password}
              description={`At least ${PASSWORD_MIN} characters.`}
              error={errors.password}
              autoFocus
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
          </>
        ) : null}
        {errors.form ? (
          <p className="form-error" role="alert">
            {errors.form}
          </p>
        ) : null}
        {token ? (
          <Button type="submit" data-variant="solid" disabled={busy}>
            {busy ? 'Saving…' : 'Change password'}
          </Button>
        ) : null}
        <p className="gate-foot">
          {linkFailed && canAskAgain ? (
            <a
              href={hrefFor(FORGOT_PATH)}
              onClick={(event) => {
                event.preventDefault()
                replaceHash(hrefFor(FORGOT_PATH))
              }}
            >
              Ask for a new link
            </a>
          ) : (
            <a
              href={hrefFor()}
              onClick={(event) => {
                event.preventDefault()
                replaceHash('')
              }}
            >
              {back}
            </a>
          )}
        </p>
      </form>
    </div>
  )
}
