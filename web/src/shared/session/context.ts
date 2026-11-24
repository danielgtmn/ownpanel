import { createContext, useContext } from 'react'
import type { User } from './types.ts'

export type Session = {
  user: User
  isAdmin: boolean
  refresh: () => Promise<void>
  signOut: () => Promise<void>
}

export const SessionContext = createContext<Session | null>(null)

export function useSession(): Session {
  const session = useContext(SessionContext)
  if (!session) throw new Error('useSession needs a signed-in person.')
  return session
}
