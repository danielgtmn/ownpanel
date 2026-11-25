import { describe, expect, it } from 'vitest'
import { attentionText, buildFamilies, familyStatus, familySummary, initialsOf, type DomainSources } from './families.ts'
import { registrableDomain } from './suffix.ts'

const empty: DomainSources = { sites: [], zones: [], redirects: [] }

describe('registrableDomain', () => {
  it('groups subdomains and www under the registrable domain', () => {
    expect(registrableDomain('blog.example.com')).toBe('example.com')
    expect(registrableDomain('www.example.com')).toBe('example.com')
    expect(registrableDomain('a.b.c.example.com')).toBe('example.com')
    expect(registrableDomain('Example.COM.')).toBe('example.com')
  })

  it('keeps common multi-part suffixes together', () => {
    expect(registrableDomain('www.shop.co.uk')).toBe('shop.co.uk')
    expect(registrableDomain('shop.co.uk')).toBe('shop.co.uk')
    expect(registrableDomain('blog.firma.com.au')).toBe('firma.com.au')
    expect(registrableDomain('mail.example.co.jp')).toBe('example.co.jp')
  })

  it('treats a short host or an address as its own family', () => {
    expect(registrableDomain('shop.de')).toBe('shop.de')
    expect(registrableDomain('localhost')).toBe('localhost')
    expect(registrableDomain('203.0.113.5')).toBe('203.0.113.5')
  })

  it('does not mistake a long generic name for a suffix', () => {
    expect(registrableDomain('www.example.company')).toBe('example.company')
    expect(registrableDomain('blog.example.localtest.me')).toBe('localtest.me')
  })
})

describe('buildFamilies', () => {
  it('collects sites, zones and redirects per registrable domain', () => {
    const families = buildFamilies({
      sites: [{ name: 'blog', domains: ['blog.example.com', 'www.example.com'], live: true }],
      zones: [{ connectionId: 1, connectionName: 'Hetzner', name: 'example.com' }],
      redirects: [{ id: 7, source: 'Go.Example.com', target: 'https://example.org' }],
    })
    expect(families).toHaveLength(1)
    const [family] = families
    expect(family.domain).toBe('example.com')
    expect(family.sites).toEqual([
      { name: 'blog', live: true, domains: ['blog.example.com', 'www.example.com'], otherFamilies: [] },
    ])
    expect(family.zones).toHaveLength(1)
    expect(family.redirects).toEqual([{ id: 7, source: 'go.example.com', target: 'https://example.org' }])
    expect(family).not.toHaveProperty('panelDomain')
    expect(family.servedDomains).toEqual(['blog.example.com', 'go.example.com', 'www.example.com'])
  })

  it('lists a site that spans several families in each, marked with the others', () => {
    const families = buildFamilies({
      ...empty,
      sites: [{ name: 'shop', domains: ['shop.de', 'www.shop.co.uk', 'shop.co.uk'], live: false }],
    })
    expect(families.map((family) => family.domain)).toEqual(['shop.co.uk', 'shop.de'])
    expect(families[0].sites[0]).toEqual({
      name: 'shop',
      live: false,
      domains: ['www.shop.co.uk', 'shop.co.uk'],
      otherFamilies: ['shop.de'],
    })
    expect(families[1].sites[0].otherFamilies).toEqual(['shop.co.uk'])
  })

  it('makes a family of a DNS domain nothing runs on yet', () => {
    const [family] = buildFamilies({ ...empty, zones: [{ connectionId: 2, connectionName: 'Work', name: 'idle.net' }] })
    expect(family.domain).toBe('idle.net')
    expect(family.servedDomains).toEqual([])
  })

  it('never makes a family of the panel address', () => {
    // Older callers may still hand in the panel's address; it must not create or change a family.
    const sources = { ...empty, panelDomain: 'panel.hosting.io' } as DomainSources
    expect(buildFamilies(sources)).toEqual([])
  })

  it('returns no families without data', () => {
    expect(buildFamilies(empty)).toEqual([])
  })
})

describe('familyStatus', () => {
  const [family] = buildFamilies({
    ...empty,
    sites: [{ name: 'blog', domains: ['example.com', 'www.example.com'], live: true }],
    redirects: [{ id: 1, source: 'go.example.com', target: 'https://x.org' }],
  })

  it('is ok when every domain points here and every site is live', () => {
    expect(
      familyStatus(family, {
        'example.com': 'points_here',
        'www.example.com': 'points_here',
        'go.example.com': 'points_here',
      }),
    ).toBe('ok')
  })

  it('needs attention when a domain points elsewhere or has no record', () => {
    expect(
      familyStatus(family, {
        'example.com': 'points_here',
        'www.example.com': 'points_elsewhere',
        'go.example.com': 'points_here',
      }),
    ).toBe('attention')
    expect(familyStatus(family, { 'example.com': 'no_record' })).toBe('attention')
  })

  it('needs attention when a site has no content, whatever DNS says', () => {
    const [offline] = buildFamilies({ ...empty, sites: [{ name: 'new', domains: ['new.org'], live: false }] })
    expect(familyStatus(offline, { 'new.org': 'points_here' })).toBe('attention')
  })

  it('is still checking while a lookup is missing or failed', () => {
    expect(familyStatus(family, {})).toBe('pending')
    expect(
      familyStatus(family, {
        'example.com': 'points_here',
        'www.example.com': 'lookup_failed',
        'go.example.com': 'points_here',
      }),
    ).toBe('pending')
  })

  it('is idle when nothing runs on the family', () => {
    const [zoneOnly] = buildFamilies({ ...empty, zones: [{ connectionId: 1, connectionName: 'H', name: 'idle.net' }] })
    expect(familyStatus(zoneOnly, {})).toBe('idle')
  })
})

describe('labels', () => {
  it('summarises a family', () => {
    const [family] = buildFamilies({
      ...empty,
      sites: [{ name: 'blog', domains: ['example.com'], live: true }],
      redirects: [
        { id: 1, source: 'a.example.com', target: 'https://x.org' },
        { id: 2, source: 'b.example.com', target: 'https://x.org' },
      ],
      zones: [{ connectionId: 1, connectionName: 'H', name: 'example.com' }],
    })
    expect(familySummary(family)).toBe('1 site · 2 redirects · DNS managed here')
  })

  it('uses two letters of the name for a tile', () => {
    expect(initialsOf('example.com')).toBe('Ex')
    expect(initialsOf('shop.co.uk')).toBe('Sh')
    expect(initialsOf('4u.de')).toBe('4u')
  })
})

describe('attentionText', () => {
  const [family] = buildFamilies({
    ...empty,
    sites: [
      { name: 'blog', domains: ['example.com', 'www.example.com'], live: false },
      { name: 'shop', domains: ['shop.example.com'], live: true },
    ],
  })

  it('names what needs to be done', () => {
    expect(attentionText(family, { 'example.com': 'no_record', 'www.example.com': 'points_here' })).toBe(
      "The site blog has no content yet, and example.com doesn't point to this server yet.",
    )
    expect(
      attentionText(family, { 'example.com': 'no_record', 'www.example.com': 'points_elsewhere', 'shop.example.com': 'points_here' }),
    ).toBe("The site blog has no content yet, and 2 addresses don't point to this server yet.")
  })

  it('names a mail domain that waits for its records', () => {
    const [mail] = buildFamilies({ ...empty, mailDomains: [{ domain: 'example.org', active: false }] })
    expect(attentionText(mail, {})).toBe('e-mail for example.org waits for its mail records.')
  })

  it('is empty when nothing needs attention', () => {
    const [live] = buildFamilies({ ...empty, sites: [{ name: 'blog', domains: ['example.com'], live: true }] })
    expect(attentionText(live, { 'example.com': 'points_here' })).toBe('')
  })
})

describe('families with e-mail', () => {
  it('makes a family for a mail domain without a site', () => {
    const families = buildFamilies({ ...empty, mailDomains: [{ domain: 'Shop.Example.org.', active: false }] })
    expect(families.map((family) => family.domain)).toEqual(['example.org'])
    expect(families[0].mailDomains).toEqual([{ domain: 'shop.example.org', active: false }])
    expect(familySummary(families[0])).toBe('E-mail · DNS managed elsewhere')
  })

  it('needs attention while a mail domain waits for its records, and is fine once it receives mail', () => {
    const [waiting] = buildFamilies({ ...empty, mailDomains: [{ domain: 'example.org', active: false }] })
    const [active] = buildFamilies({ ...empty, mailDomains: [{ domain: 'example.org', active: true }] })
    expect(familyStatus(waiting, {})).toBe('attention')
    expect(familyStatus(active, {})).toBe('ok')
  })

  it('works without mail data', () => {
    const [family] = buildFamilies({ ...empty, zones: [{ connectionId: 1, connectionName: 'x', name: 'example.org' }] })
    expect(family.mailDomains).toEqual([])
    expect(familyStatus(family, {})).toBe('idle')
  })
})
