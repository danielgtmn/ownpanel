import { describe, expect, it } from 'vitest'
import {
  addressActions,
  addressState,
  checksOnLoad,
  collectAddresses,
  recordHost,
  subdomainHost,
  type Address,
} from './addresses.ts'
import type { DomainSources } from './families.ts'

const empty: DomainSources = { sites: [], zones: [], redirects: [] }
const hosts = (list: Address[]) => list.map((address) => address.host)

describe('collectAddresses', () => {
  it('always lists the domain and www first, even when nothing uses them', () => {
    expect(collectAddresses('example.com', empty)).toEqual([
      { host: 'example.com', wildcard: false, use: { kind: 'none' } },
      { host: 'www.example.com', wildcard: false, use: { kind: 'none' } },
    ])
  })

  it('collects the addresses of sites and redirects with what they serve', () => {
    const list = collectAddresses('example.com', {
      sites: [
        { name: 'main', domains: ['example.com', 'www.example.com'], live: true },
        { name: 'blog', domains: ['Blog.Example.com.'], live: false },
      ],
      zones: [],
      redirects: [{ id: '3', source: 'go.example.com', target: 'https://example.org' }],
    })
    expect(list).toEqual([
      { host: 'example.com', wildcard: false, use: { kind: 'site', site: 'main', live: true } },
      { host: 'www.example.com', wildcard: false, use: { kind: 'site', site: 'main', live: true } },
      { host: 'blog.example.com', wildcard: false, use: { kind: 'site', site: 'blog', live: false } },
      { host: 'go.example.com', wildcard: false, use: { kind: 'redirect', id: '3', target: 'https://example.org' } },
    ])
  })

  it('adds the domains of path redirects as addresses without a use of their own', () => {
    const list = collectAddresses('example.com', { ...empty, pathRedirectHosts: ['docs.example.com'] })
    expect(list.find((address) => address.host === 'docs.example.com')?.use).toEqual({ kind: 'none' })
  })

  it('adds names of address records from DNS, skipping service names and other record types', () => {
    const list = collectAddresses('example.com', empty, [
      {
        zone: 'example.com',
        records: [
          { name: '@', type: 'A' },
          { name: '@', type: 'MX' },
          { name: 'shop', type: 'A' },
          { name: 'api', type: 'AAAA' },
          { name: 'cdn', type: 'CNAME' },
          { name: 'mail', type: 'MX' },
          { name: 'txtonly', type: 'TXT' },
          { name: 'sub', type: 'NS' },
          { name: '_dmarc', type: 'TXT' },
          { name: '_acme-challenge.shop', type: 'CNAME' },
          { name: '*', type: 'A' },
        ],
      },
    ])
    expect(hosts(list)).toEqual([
      'example.com',
      'www.example.com',
      '*.example.com',
      'api.example.com',
      'cdn.example.com',
      'shop.example.com',
    ])
    expect(list.find((address) => address.host === '*.example.com')).toEqual({
      host: '*.example.com',
      wildcard: true,
      use: { kind: 'none' },
    })
  })

  it('lists each address once and keeps the first use', () => {
    const list = collectAddresses(
      'example.com',
      {
        sites: [
          { name: 'zeta', domains: ['shop.example.com'], live: true },
          { name: 'alpha', domains: ['shop.example.com'], live: false },
        ],
        zones: [],
        redirects: [],
      },
      [{ zone: 'example.com', records: [{ name: 'shop', type: 'A' }, { name: 'SHOP', type: 'AAAA' }, { name: 'www', type: 'CNAME' }] }],
    )
    expect(hosts(list)).toEqual(['example.com', 'www.example.com', 'shop.example.com'])
    expect(list[2].use).toEqual({ kind: 'site', site: 'alpha', live: false })
  })

  it('leaves out addresses of other domains', () => {
    const list = collectAddresses(
      'example.com',
      {
        sites: [{ name: 'shop', domains: ['shop.example.com', 'example.org', 'example.com.au'], live: true }],
        zones: [],
        redirects: [{ id: '1', source: 'go.other.com', target: 'https://example.com' }],
        pathRedirectHosts: ['notexample.com'],
      },
      [{ zone: 'other.com', records: [{ name: 'x', type: 'A' }] }],
    )
    expect(hosts(list)).toEqual(['example.com', 'www.example.com', 'shop.example.com'])
  })

  it('sorts subdomains alphabetically after the domain and www', () => {
    const list = collectAddresses('example.com', {
      ...empty,
      pathRedirectHosts: ['b.example.com', 'www.example.com', 'a.b.example.com', 'example.com', 'aa.example.com'],
    })
    expect(hosts(list)).toEqual(['example.com', 'www.example.com', 'a.b.example.com', 'aa.example.com', 'b.example.com'])
  })
})

describe('recordHost', () => {
  it('turns record names into full addresses', () => {
    expect(recordHost('example.com', '@')).toBe('example.com')
    expect(recordHost('example.com', '')).toBe('example.com')
    expect(recordHost('example.com', 'Shop')).toBe('shop.example.com')
    expect(recordHost('example.com', 'a.b')).toBe('a.b.example.com')
    expect(recordHost('example.com', 'full.example.com.')).toBe('full.example.com')
  })
})

describe('address states', () => {
  const site: Address = { host: 'example.com', wildcard: false, use: { kind: 'site', site: 'main', live: true } }
  const redirect: Address = { host: 'go.example.com', wildcard: false, use: { kind: 'redirect', id: '1', target: 'https://x.org' } }
  const unused: Address = { host: 'shop.example.com', wildcard: false, use: { kind: 'none' } }
  const wildcard: Address = { host: '*.example.com', wildcard: true, use: { kind: 'none' } }

  it('maps what an address serves to its state', () => {
    expect(addressState(site)).toBe('website')
    expect(addressState(redirect)).toBe('redirect')
    expect(addressState(unused)).toBe('unused')
    expect(addressState(wildcard)).toBe('wildcard')
  })

  it('offers actions only on unused addresses, and adding to a website only when there is one', () => {
    expect(addressActions(site, 2)).toEqual([])
    expect(addressActions(redirect, 2)).toEqual([])
    expect(addressActions(wildcard, 2)).toEqual([])
    expect(addressActions(unused, 2)).toEqual(['add-website', 'add-redirect', 'add-to-website'])
    expect(addressActions(unused, 0)).toEqual(['add-website', 'add-redirect'])
  })

  it('checks where an address points right away only when it serves something', () => {
    expect(checksOnLoad(site)).toBe(true)
    expect(checksOnLoad(redirect)).toBe(true)
    expect(checksOnLoad(unused)).toBe(false)
    expect(checksOnLoad(wildcard)).toBe(false)
  })
})

describe('subdomainHost', () => {
  it('adds the domain to a valid label', () => {
    expect(subdomainHost('shop', 'example.com')).toEqual({ host: 'shop.example.com' })
    expect(subdomainHost(' Shop-2 ', 'example.com')).toEqual({ host: 'shop-2.example.com' })
    expect(subdomainHost('eu.shop', 'example.com')).toEqual({ host: 'eu.shop.example.com' })
    expect(subdomainHost('shop.example.com', 'example.com')).toEqual({ host: 'shop.example.com' })
  })

  it('refuses what the server would refuse', () => {
    for (const label of ['', ' ', '-shop', 'shop-', 'sh op', 'shop_1', '*', 'a..b', '.shop', 'shöp', 'a'.repeat(64)]) {
      expect(subdomainHost(label, 'example.com'), label).toHaveProperty('error')
    }
  })

  it('accepts a part of 63 characters', () => {
    expect(subdomainHost('a'.repeat(63), 'example.com')).toEqual({ host: `${'a'.repeat(63)}.example.com` })
  })
})
