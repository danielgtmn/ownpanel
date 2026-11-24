import { describe, expect, it } from 'vitest'
import { parseDomainInput, validateDomains, validateSiteName } from './validation.ts'

describe('validateSiteName', () => {
  it.each(['a', 'my-site', 'site-2', '9lives', 'a'.repeat(63)])('accepts %s', (name) => {
    expect(validateSiteName(name)).toBeNull()
  })

  it('asks for a name when empty', () => {
    expect(validateSiteName('  ')).toMatch(/enter a name/i)
  })

  it('rejects uppercase letters', () => {
    expect(validateSiteName('MySite')).toMatch(/lowercase/i)
  })

  it('rejects spaces and symbols', () => {
    expect(validateSiteName('my site')).toMatch(/only lowercase letters, numbers and hyphens/i)
    expect(validateSiteName('my_site')).toMatch(/only lowercase letters, numbers and hyphens/i)
  })

  it('rejects hyphens at the edges', () => {
    expect(validateSiteName('-site')).toMatch(/start and end/i)
    expect(validateSiteName('site-')).toMatch(/start and end/i)
  })

  it('rejects names over 63 characters', () => {
    expect(validateSiteName('a'.repeat(64))).toMatch(/at most 63/i)
  })
})

describe('parseDomainInput', () => {
  it('splits on lines and commas, trims, lowercases and drops empties', () => {
    expect(parseDomainInput(' Example.com,\n\n www.example.com ,, ')).toEqual(['example.com', 'www.example.com'])
  })

  it('removes duplicates keeping the first', () => {
    expect(parseDomainInput('a.com\nA.com\nb.com')).toEqual(['a.com', 'b.com'])
  })

  it('returns an empty list for blank input', () => {
    expect(parseDomainInput('  \n ')).toEqual([])
  })
})

describe('validateDomains', () => {
  it('accepts plain hostnames', () => {
    expect(validateDomains(['example.com', 'www.my-site.co.uk', 'xn--bcher-kva.example'])).toBeNull()
    expect(validateDomains([])).toBeNull()
  })

  it('rejects names without a dot', () => {
    expect(validateDomains(['localhost'])).toMatch(/isn't a valid domain/)
  })

  it('points out URLs and paths', () => {
    expect(validateDomains(['https://example.com'])).toMatch(/without https/)
    expect(validateDomains(['example.com/blog'])).toMatch(/without https/)
  })

  it('rejects invalid characters and edge hyphens', () => {
    expect(validateDomains(['exa mple.com'])).not.toBeNull()
    expect(validateDomains(['-bad.example.com'])).not.toBeNull()
    expect(validateDomains(['example..com'])).not.toBeNull()
  })

  it('reports the first bad entry', () => {
    expect(validateDomains(['ok.com', 'bad'])).toContain('"bad"')
  })
})
