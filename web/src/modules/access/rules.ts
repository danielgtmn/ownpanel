import { formatDateTime } from '../../shared/format/index.ts'
import type { NewToken, Token, TokenAccess } from './types.ts'

export type SiteScope = 'all' | 'specific'

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

export type TokenDraft = {
  name: string
  access: TokenAccess
  scope: SiteScope
  picked: string[]
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
  }
}
