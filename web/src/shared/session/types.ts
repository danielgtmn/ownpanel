export type Role = 'admin' | 'member'

export type User = {
  id: number
  name: string
  // "" when the person has none.
  email: string
  role: Role
  created_at: string
}

export type SessionInfo = {
  setup_required: boolean
  setup_complete?: boolean
  user: User | null
  via: 'session' | 'token' | null
  // Whether a forgotten password can be reset with a link sent by e-mail.
  password_reset?: boolean
}

export const ROLE_LABEL: Record<Role, string> = {
  admin: 'Administrator',
  member: 'Member',
}
