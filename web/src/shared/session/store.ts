import { ApiError, errorMessage, request, setUnauthorizedHandler } from '../api/index.ts'
import type { SessionInfo, User } from './types.ts'

export type SessionState =
  | { status: 'loading' }
  | { status: 'unreachable'; message: string }
  | { status: 'setup' }
  | { status: 'signedOut'; notice: string | null; passwordReset: boolean }
  | { status: 'signedIn'; user: User; setupComplete: boolean }

export const SIGNED_OUT_NOTICE = 'You were signed out. Sign in again to continue where you left off.'
export const PASSWORD_RESET_NOTICE = 'Your password was changed. Sign in with the new one.'

const listeners = new Set<() => void>()
let state: SessionState = { status: 'loading' }
// Remembered from the last answer about the session, for the sign-in screen after signing out.
let passwordReset = false

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
  return { status: 'signedOut', notice, passwordReset: info.password_reset === true }
}

async function readSession(): Promise<SessionInfo> {
  const info = await request<SessionInfo>('/session', { silentUnauthorized: true })
  passwordReset = info.password_reset === true
  return info
}

function signedOut(notice: string | null): SessionState {
  return { status: 'signedOut', notice, passwordReset }
}

export async function loadSession(notice: string | null = null): Promise<void> {
  try {
    const info = await readSession()
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
    const info = await readSession()
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
  update(signedOut(null))
}

export async function signOutEverywhereNow(): Promise<void> {
  await request<void>('/account/logout-everywhere', { method: 'POST', silentUnauthorized: true })
  update(signedOut(null))
}

export function endSession() {
  if (state.status === 'signedIn') update(signedOut(SIGNED_OUT_NOTICE))
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
        const info = await readSession()
        if (!info.user) endSession()
      } catch {
        // Could not tell; the original message is shown.
      }
    }
    throw error
  }
}

// Sets or, with "", removes the signed-in person's own address. The server
// asks for the current password so an unattended login cannot redirect
// password reset links.
export async function setOwnEmail(email: string, current: string): Promise<User> {
  const user = await request<User>('/account/email', { method: 'PUT', body: { email, current } })
  if (state.status === 'signedIn' && state.user.id === user.id) update({ ...state, user })
  return user
}

// The server answers the same whether or not the address belongs to someone.
export async function requestPasswordReset(email: string): Promise<void> {
  await request<void>('/password-reset', { method: 'POST', body: { email }, silentUnauthorized: true })
}

// Every login of that person ends, so the session is read again afterwards.
export async function resetPassword(token: string, password: string): Promise<void> {
  await request<void>('/password-reset/confirm', {
    method: 'POST',
    body: { token, password },
    silentUnauthorized: true,
  })
  await loadSession(PASSWORD_RESET_NOTICE)
}

setUnauthorizedHandler(endSession)
