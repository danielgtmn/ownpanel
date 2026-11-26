export type TokenAccess = 'read' | 'write'

export type Token = {
  id: string
  name: string
  access: TokenAccess
  sites: string[] | null
  created_at: string
  last_used_at: string | null
  expires_at: string | null
  expired: boolean
}

export type CreatedToken = Token & { token: string }

export type NewToken = {
  name: string
  access: TokenAccess
  sites: string[] | null
  expires_in_days: number | null
}
