import { toast } from '@ui-sdk/react/toast'
import { useCallback, useMemo, type ReactNode } from 'react'
import { errorMessage } from '../api/index.ts'
import { SessionContext, type Session } from './context.ts'
import { loadSession, signOutNow } from './store.ts'
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

  const value = useMemo<Session>(
    () => ({ user, isAdmin: user.role === 'admin', refresh: () => loadSession(), signOut }),
    [user, signOut],
  )
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
