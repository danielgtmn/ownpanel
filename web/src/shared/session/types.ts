export type Role = 'admin' | 'member'

export type User = {
  id: number
  name: string
  role: Role
  created_at: string
}

export type SessionInfo = {
  setup_required: boolean
  setup_complete?: boolean
  user: User | null
  via: 'session' | 'token' | null
}

export const ROLE_LABEL: Record<Role, string> = {
  admin: 'Administrator',
  member: 'Member',
}
