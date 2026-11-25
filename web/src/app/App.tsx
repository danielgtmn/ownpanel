import { OverlayHost } from '@ui-sdk/react/overlays'
import { useEffect, useSyncExternalStore } from 'react'
import { ErrorState, LoadingState } from '../shared/ui/index.ts'
import { getSessionState, loadSession, SessionProvider, subscribeSession } from '../shared/session/index.ts'
import { screenFor } from './screen.ts'
import { lazyPage } from './lazyPage.ts'
import { PageFrame } from './ModulePage.tsx'
import { SetupScreen } from './SetupScreen.tsx'
import { Shell } from './Shell.tsx'
import { SignInScreen } from './SignInScreen.tsx'

// The first-run wizard is shown once per panel, so its code loads only then.
const SetupWizard = lazyPage(() => import('./setup/SetupWizard.tsx').then((page) => page.SetupWizard))

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
          {screenFor(state) === 'wizard' ? (
            <PageFrame frame="gate">
              <SetupWizard />
            </PageFrame>
          ) : (
            <Shell />
          )}
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
