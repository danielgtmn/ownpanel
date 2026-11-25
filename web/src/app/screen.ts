import type { Route } from '../shared/routing/index.ts'
import type { SessionState } from '../shared/session/index.ts'

export type ScreenId = 'loading' | 'unreachable' | 'setup' | 'signedOut' | 'wizard' | 'panel'

// Administrators finish the first-run wizard before they see the panel; everybody else goes straight in.
export function screenFor(state: SessionState): ScreenId {
  if (state.status !== 'signedIn') return state.status
  return state.user.role === 'admin' && !state.setupComplete ? 'wizard' : 'panel'
}

export const RESET_PATH = 'reset-password'
export const FORGOT_PATH = 'forgot-password'

// The token of a link to choose a new password ("" when it's missing), or null on any other address.
export function resetTokenFrom(route: Route): string | null {
  if (route.segments[0] !== RESET_PATH) return null
  return (route.query.get('token') ?? '').trim()
}

// The sign-in screen asks for an e-mail address instead when the address says so and the server can send links.
export function showsForgotForm(route: Route, passwordReset: boolean): boolean {
  return passwordReset && route.segments[0] === FORGOT_PATH
}
