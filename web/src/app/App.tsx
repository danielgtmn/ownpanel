import { OverlayHost } from '@ui-sdk/react/overlays'
import { useEffect, useSyncExternalStore } from 'react'
import { accountHost } from '../account/accountHost.ts'
import { ErrorState, LoadingState } from '../shared/ui/index.ts'
import { getSessionState, loadSession, SessionProvider, subscribeSession } from '../shared/session/index.ts'
import { replaceHash, useRoute } from '../shared/routing/index.ts'
import { FORGOT_PATH, resetTokenFrom, screenFor, showsForgotForm } from './screen.ts'
import { lazyPage } from './lazyPage.ts'
import { PageFrame } from './ModulePage.tsx'
import { ResetPasswordScreen } from './ResetPasswordScreen.tsx'
import { SetupScreen } from './SetupScreen.tsx'
import { Shell } from './Shell.tsx'
import { SignInScreen } from './SignInScreen.tsx'

// The first-run wizard is shown once per panel, so its code loads only then.
const SetupWizard = lazyPage(() => import('./setup/SetupWizard.tsx').then((page) => page.SetupWizard))
// Mailbox users on the mail server's name never see the panel, nor load its session.
const AccountScreen = lazyPage(() => import('../account/AccountScreen.tsx').then((page) => page.AccountScreen))
const mailHost = accountHost()

function Screen() {
  const state = useSyncExternalStore(subscribeSession, getSessionState)
  const route = useRoute()
  const resetToken = resetTokenFrom(route)

  useEffect(() => {
    void loadSession()
  }, [])

  const leftForgotForm = state.status === 'signedIn' && route.segments[0] === FORGOT_PATH
  useEffect(() => {
    if (leftForgotForm) replaceHash('')
  }, [leftForgotForm])

  // A link to choose a new password works whether or not someone is signed in in this browser.
  if (resetToken !== null && (state.status === 'signedOut' || state.status === 'signedIn')) {
    return (
      <ResetPasswordScreen
        token={resetToken}
        canAskAgain={state.status === 'signedOut' && state.passwordReset}
        signedIn={state.status === 'signedIn'}
      />
    )
  }

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
      return (
        <SignInScreen
          notice={state.notice}
          passwordReset={state.passwordReset}
          forgot={showsForgotForm(route, state.passwordReset)}
        />
      )
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
      {mailHost ? (
        <PageFrame frame="gate">
          <AccountScreen host={mailHost} />
        </PageFrame>
      ) : (
        <Screen />
      )}
      <OverlayHost />
    </>
  )
}
