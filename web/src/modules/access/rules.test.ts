import { describe, expect, it } from 'vitest'
import { accessLabel, lastUsedLabel, scopeLabel, toNewToken, validateTokenDraft } from './rules.ts'

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

  it('says when a token was never used', () => {
    expect(lastUsedLabel({ last_used_at: null })).toBe('Never used')
    expect(lastUsedLabel({ last_used_at: '2026-01-02T10:00:00Z' })).toMatch(/^Last used /)
  })
})

describe('validateTokenDraft', () => {
  it('asks for a name', () => {
    expect(validateTokenDraft({ name: '  ', access: 'read', scope: 'all', picked: [] }).name).toBeTruthy()
  })

  it('needs at least one site when limited to specific sites', () => {
    expect(validateTokenDraft({ name: 'ci', access: 'read', scope: 'specific', picked: [] }).sites).toBeTruthy()
    expect(validateTokenDraft({ name: 'ci', access: 'read', scope: 'all', picked: [] })).toEqual({})
  })
})

describe('toNewToken', () => {
  it('sends null for all sites', () => {
    expect(toNewToken({ name: ' ci ', access: 'write', scope: 'all', picked: ['a'] }, ['a', 'b'])).toEqual({
      name: 'ci',
      access: 'write',
      sites: null,
    })
  })

  it('sends the picked sites in list order and drops unknown ones', () => {
    expect(toNewToken({ name: 'ci', access: 'read', scope: 'specific', picked: ['b', 'a', 'gone'] }, ['a', 'b']).sites).toEqual([
      'a',
      'b',
    ])
  })
})
