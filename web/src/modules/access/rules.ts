import { formatDateTime } from '../../shared/format/index.ts'
import type { NewToken, Token, TokenAccess } from './types.ts'

export type SiteScope = 'all' | 'specific'

/** How long a new token works: a number of days, or never. */
export type TokenExpiry = '30' | '90' | '365' | 'never'

export const EXPIRY_OPTIONS: Array<{ value: TokenExpiry; label: string }> = [
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '365', label: '1 year' },
  { value: 'never', label: 'Never' },
]

export const DEFAULT_EXPIRY: TokenExpiry = '90'

export function accessLabel(access: TokenAccess): string {
  return access === 'write' ? 'Read and change' : 'Read only'
}

export function scopeLabel(sites: string[] | null): string {
  if (sites === null) return 'All my sites'
  if (sites.length === 0) return 'No sites'
  return sites.join(', ')
}

export function lastUsedLabel(token: Pick<Token, 'last_used_at'>): string {
  return token.last_used_at ? `Last used ${formatDateTime(token.last_used_at)}` : 'Never used'
}

export function expiryLabel(token: Pick<Token, 'expires_at' | 'expired'>): string {
  if (!token.expires_at) return 'Never expires'
  const when = formatDateTime(token.expires_at)
  return token.expired ? `Expired ${when}` : `Expires ${when}`
}

export type TokenDraft = {
  name: string
  access: TokenAccess
  scope: SiteScope
  picked: string[]
  expiry: TokenExpiry
}

export type TokenDraftErrors = { name?: string; sites?: string }

export function validateTokenDraft(draft: TokenDraft): TokenDraftErrors {
  const errors: TokenDraftErrors = {}
  if (!draft.name.trim()) errors.name = 'Give the token a name so you can recognise it later.'
  if (draft.scope === 'specific' && draft.picked.length === 0) errors.sites = 'Pick at least one site.'
  return errors
}

export function toNewToken(draft: TokenDraft, allSites: string[]): NewToken {
  return {
    name: draft.name.trim(),
    access: draft.access,
    sites: draft.scope === 'all' ? null : allSites.filter((site) => draft.picked.includes(site)),
    expires_in_days: draft.expiry === 'never' ? null : Number(draft.expiry),
  }
}
