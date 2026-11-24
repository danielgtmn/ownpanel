import { request } from '../../shared/api/index.ts'
import type { CreatedToken, NewToken, Token } from './types.ts'

export function listTokens(): Promise<Token[]> {
  return request<Token[]>('/tokens')
}

export function createToken(token: NewToken): Promise<CreatedToken> {
  return request<CreatedToken>('/tokens', { method: 'POST', body: token })
}

export function deleteToken(id: number): Promise<void> {
  return request<void>(`/tokens/${id}`, { method: 'DELETE' })
}
