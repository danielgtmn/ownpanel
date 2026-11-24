export type TokenAccess = 'read' | 'write'

export type Token = {
  id: number
  name: string
  access: TokenAccess
  sites: string[] | null
  created_at: string
  last_used_at: string | null
}

export type CreatedToken = Token & { token: string }

export type NewToken = {
  name: string
  access: TokenAccess
  sites: string[] | null
}
