import type { SessionState } from '../shared/session/index.ts'

export type ScreenId = 'loading' | 'unreachable' | 'setup' | 'signedOut' | 'wizard' | 'panel'

// Administrators finish the first-run wizard before they see the panel; everybody else goes straight in.
export function screenFor(state: SessionState): ScreenId {
  if (state.status !== 'signedIn') return state.status
  return state.user.role === 'admin' && !state.setupComplete ? 'wizard' : 'panel'
}
