import { describe, expect, it } from 'vitest'
import { newRedirectHref, newSiteHref, prefilledDomain } from './prefill.ts'
import { parseRoute } from './routing.ts'

describe('prefilled dialog addresses', () => {
  it('builds addresses that open the dialogs with a domain', () => {
    expect(newSiteHref()).toBe('#/sites?new=1')
    expect(newSiteHref('shop.example.com')).toBe('#/sites?new=1&domain=shop.example.com')
    expect(newRedirectHref('go.example.com')).toBe('#/redirects?new=1&domain=go.example.com')
  })

  it('reads the domain back from the address', () => {
    const route = parseRoute(newSiteHref('shop.example.com'))
    expect(route.segments).toEqual(['sites'])
    expect(route.query.has('new')).toBe(true)
    expect(prefilledDomain(route.query)).toBe('shop.example.com')
    expect(prefilledDomain(new URLSearchParams('domain=Shop.Example.com.'))).toBe('shop.example.com')
  })

  it('ignores anything that is not a plain domain', () => {
    for (const value of ['', 'localhost', 'https://x.com', 'x.com/path', '-a.com', 'a b.com', '<b>.com']) {
      expect(prefilledDomain(new URLSearchParams({ domain: value })), value).toBeUndefined()
    }
    expect(prefilledDomain(new URLSearchParams('new=1'))).toBeUndefined()
  })
})
