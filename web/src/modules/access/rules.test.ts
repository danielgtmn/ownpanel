import { describe, expect, it } from 'vitest'
import { accessLabel, expiryLabel, lastUsedLabel, scopeLabel, toNewToken, validateTokenDraft } from './rules.ts'

describe('token labels', () => {
  it('describes what a token may do', () => {
    expect(accessLabel('read')).toBe('Read only')
    expect(accessLabel('write')).toBe('Read and change')
  })

  it('describes which sites a token reaches', () => {
    expect(scopeLabel(null)).toBe('All my sites')
    expect(scopeLabel(['blog', 'shop'])).toBe('blog, shop')
    expect(scopeLabel([])).toBe('No sites')
  })

  it('says when a token stops working', () => {
    expect(expiryLabel({ expires_at: null, expired: false })).toBe('Never expires')
    expect(expiryLabel({ expires_at: '2026-01-02T10:00:00Z', expired: false })).toMatch(/^Expires /)
    expect(expiryLabel({ expires_at: '2026-01-02T10:00:00Z', expired: true })).toMatch(/^Expired /)
  })

  it('says when a token was never used', () => {
    expect(lastUsedLabel({ last_used_at: null })).toBe('Never used')
    expect(lastUsedLabel({ last_used_at: '2026-01-02T10:00:00Z' })).toMatch(/^Last used /)
  })
})

describe('validateTokenDraft', () => {
  it('asks for a name', () => {
    expect(validateTokenDraft({ name: '  ', access: 'read', scope: 'all', picked: [], expiry: '90' }).name).toBeTruthy()
  })

  it('needs at least one site when limited to specific sites', () => {
    expect(validateTokenDraft({ name: 'ci', access: 'read', scope: 'specific', picked: [], expiry: '90' }).sites).toBeTruthy()
    expect(validateTokenDraft({ name: 'ci', access: 'read', scope: 'all', picked: [], expiry: '90' })).toEqual({})
  })
})

describe('toNewToken', () => {
  it('sends null for all sites', () => {
    expect(toNewToken({ name: ' ci ', access: 'write', scope: 'all', picked: ['a'], expiry: '30' }, ['a', 'b'])).toEqual({
      name: 'ci',
      access: 'write',
      sites: null,
      expires_in_days: 30,
    })
  })

  it('sends null days for a token that never expires', () => {
    expect(toNewToken({ name: 'ci', access: 'read', scope: 'all', picked: [], expiry: 'never' }, []).expires_in_days).toBeNull()
    expect(toNewToken({ name: 'ci', access: 'read', scope: 'all', picked: [], expiry: '365' }, []).expires_in_days).toBe(365)
  })

  it('sends the picked sites in list order and drops unknown ones', () => {
    expect(toNewToken({ name: 'ci', access: 'read', scope: 'specific', picked: ['b', 'a', 'gone'], expiry: 'never' }, ['a', 'b']).sites).toEqual([
      'a',
      'b',
    ])
  })
})
