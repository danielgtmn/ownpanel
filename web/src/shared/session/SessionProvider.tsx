import { confirm } from '@ui-sdk/react/alert-dialog'
import { toast } from '@ui-sdk/react/toast'
import { useCallback, useMemo, type ReactNode } from 'react'
import { errorMessage } from '../api/index.ts'
import { SessionContext, type Session } from './context.ts'
import { loadSession, signOutEverywhereNow, signOutNow } from './store.ts'
import type { User } from './types.ts'

export function SessionProvider({ user, children }: { user: User; children: ReactNode }) {
  const signOut = useCallback(async () => {
    try {
      await signOutNow()
      window.location.hash = ''
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }, [])

  const signOutEverywhere = useCallback(async () => {
    const yes = await confirm({
      title: 'Sign out everywhere?',
      description:
        'You will be signed out on every device and browser, this one included. Access tokens keep working.',
      confirmLabel: 'Sign out everywhere',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await signOutEverywhereNow()
      window.location.hash = ''
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }, [])

  const value = useMemo<Session>(
    () => ({ user, isAdmin: user.role === 'admin', refresh: () => loadSession(), signOut, signOutEverywhere }),
    [user, signOut, signOutEverywhere],
  )
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
