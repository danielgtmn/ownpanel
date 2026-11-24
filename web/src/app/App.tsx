import { OverlayHost } from '@ui-sdk/react/overlays'
import { useEffect, useSyncExternalStore } from 'react'
import { ErrorState, LoadingState } from '../shared/ui/index.ts'
import { getSessionState, loadSession, SessionProvider, subscribeSession } from '../shared/session/index.ts'
import { screenFor } from './screen.ts'
import { SetupWizard } from './setup/SetupWizard.tsx'
import { SetupScreen } from './SetupScreen.tsx'
import { Shell } from './Shell.tsx'
import { SignInScreen } from './SignInScreen.tsx'

function Screen() {
  const state = useSyncExternalStore(subscribeSession, getSessionState)

  useEffect(() => {
    void loadSession()
  }, [])

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
          <ErrorState title="Can't open the panel" message={state.message} onRetry={() => void loadSession()} />
        </div>
      )
    case 'setup':
      return <SetupScreen />
    case 'signedOut':
      return <SignInScreen notice={state.notice} />
    case 'signedIn':
      return (
        <SessionProvider user={state.user}>
          {screenFor(state) === 'wizard' ? <SetupWizard /> : <Shell />}
        </SessionProvider>
      )
  }
}

export function App() {
  return (
    <>
      <Screen />
      <OverlayHost />
    </>
  )
}
