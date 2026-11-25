import { ApiError, errorMessage, request, setUnauthorizedHandler } from '../api/index.ts'
import type { SessionInfo, User } from './types.ts'

export type SessionState =
  | { status: 'loading' }
  | { status: 'unreachable'; message: string }
  | { status: 'setup' }
  | { status: 'signedOut'; notice: string | null }
  | { status: 'signedIn'; user: User; setupComplete: boolean }

export const SIGNED_OUT_NOTICE = 'You were signed out. Sign in again to continue where you left off.'

const listeners = new Set<() => void>()
let state: SessionState = { status: 'loading' }

function update(next: SessionState) {
  state = next
  listeners.forEach((listener) => listener())
}

export function getSessionState(): SessionState {
  return state
}

export function subscribeSession(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function toSessionState(info: SessionInfo, notice: string | null = null): SessionState {
  if (info.user) return { status: 'signedIn', user: info.user, setupComplete: info.setup_complete !== false }
  if (info.setup_required) return { status: 'setup' }
  return { status: 'signedOut', notice }
}

export async function loadSession(notice: string | null = null): Promise<void> {
  try {
    const info = await request<SessionInfo>('/session', { silentUnauthorized: true })
    update(toSessionState(info, notice))
  } catch (error) {
    if (state.status === 'loading' || state.status === 'unreachable') {
      update({ status: 'unreachable', message: errorMessage(error) })
    }
  }
}

export async function signIn(name: string, password: string): Promise<void> {
  const user = await request<User>('/login', { method: 'POST', body: { name, password }, silentUnauthorized: true })
  update({ status: 'signedIn', user, setupComplete: await readSetupComplete() })
}

export async function completeSetup(setupCode: string, name: string, password: string): Promise<void> {
  const user = await request<User>('/setup', {
    method: 'POST',
    body: { setup_code: setupCode, name, password },
    silentUnauthorized: true,
  })
  update({ status: 'signedIn', user, setupComplete: false })
}

async function readSetupComplete(): Promise<boolean> {
  try {
    const info = await request<SessionInfo>('/session', { silentUnauthorized: true })
    return info.setup_complete !== false
  } catch {
    // Unknown: open the panel itself; an administrator can finish the setup under Settings.
    return true
  }
}

export function markSetupComplete() {
  if (state.status === 'signedIn' && !state.setupComplete) update({ ...state, setupComplete: true })
}

export async function signOutNow(): Promise<void> {
  await request<void>('/logout', { method: 'POST', silentUnauthorized: true })
  update({ status: 'signedOut', notice: null })
}

export async function signOutEverywhereNow(): Promise<void> {
  await request<void>('/account/logout-everywhere', { method: 'POST', silentUnauthorized: true })
  update({ status: 'signedOut', notice: null })
}

export function endSession() {
  if (state.status === 'signedIn') update({ status: 'signedOut', notice: SIGNED_OUT_NOTICE })
}

export async function changePassword(current: string, next: string): Promise<void> {
  try {
    await request<void>('/account/password', {
      method: 'PUT',
      body: { current, new: next },
      silentUnauthorized: true,
    })
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      // A 401 here can mean a wrong current password or an ended login; ask which.
      try {
        const info = await request<SessionInfo>('/session', { silentUnauthorized: true })
        if (!info.user) endSession()
      } catch {
        // Could not tell; the original message is shown.
      }
    }
    throw error
  }
}

setUnauthorizedHandler(endSession)
