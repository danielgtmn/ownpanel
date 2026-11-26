import { useCallback, useEffect, useState } from 'react'
import { ErrorState, LoadingState } from '../shared/ui/index.ts'
import { AccountPage } from './AccountPage.tsx'
import { accountErrorMessage, isSessionEnded, loadMe, onSessionEnded, SESSION_ENDED, type Me } from './api.ts'
import { SignInForm } from './SignInForm.tsx'
import './account.css'

type State =
  | { status: 'loading' }
  | { status: 'unreachable'; message: string }
  | { status: 'signedOut'; notice: string | null }
  | { status: 'signedIn'; me: Me }

// The account area on the mail server's name, where mailbox users sign in
// to manage their own mailbox (wiki/Groupware-Architecture.md, G1). It talks
// only to the mail server's own API, never to the panel's.
export function AccountScreen({ host }: { host: string }) {
  const [state, setState] = useState<State>({ status: 'loading' })

  const load = useCallback(
    () =>
      loadMe(true).then(
        (me) => setState({ status: 'signedIn', me }),
        (error: unknown) =>
          setState(
            isSessionEnded(error)
              ? { status: 'signedOut', notice: null }
              : { status: 'unreachable', message: accountErrorMessage(error) },
          ),
      ),
    [],
  )

  useEffect(() => {
    void load()
  }, [load])

  // Any request that finds the session gone leads back to signing in.
  useEffect(() => {
    onSessionEnded(() => setState({ status: 'signedOut', notice: SESSION_ENDED }))
    return () => onSessionEnded(null)
  }, [])

  // Coming back to a page left open: check that the session still holds.
  const signedIn = state.status === 'signedIn'
  useEffect(() => {
    if (!signedIn) return
    function onVisible() {
      if (document.visibilityState !== 'visible') return
      loadMe().then(
        (me) => setState((current) => (current.status === 'signedIn' ? { status: 'signedIn', me } : current)),
        () => undefined,
      )
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [signedIn])

  useEffect(() => {
    document.title = state.status === 'signedIn' ? `${state.me.address} · ${host}` : `Sign in to your mailbox · ${host}`
  }, [state, host])

  switch (state.status) {
    case 'loading':
      return (
        <div className="gate">
          <LoadingState label="Loading…" />
        </div>
      )
    case 'unreachable':
      return (
        <div className="gate">
          <ErrorState
            title="Can't open your mailbox's page"
            message={state.message}
            onRetry={() => {
              setState({ status: 'loading' })
              void load()
            }}
          />
        </div>
      )
    case 'signedOut':
      return (
        <SignInForm host={host} notice={state.notice} onSignedIn={(me) => setState({ status: 'signedIn', me })} />
      )
    case 'signedIn':
      return (
        <AccountPage
          host={host}
          me={state.me}
          onChange={(me) => setState({ status: 'signedIn', me })}
          onSignedOut={() => setState({ status: 'signedOut', notice: 'You signed out.' })}
        />
      )
  }
}
