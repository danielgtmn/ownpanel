import { afterEach, describe, expect, it, vi } from 'vitest'
import { buildFamilies, type DomainSources, type Family } from '../../shared/families/index.ts'
import { copyDomain, iconUrl, mainSite, openSite, siteAddress } from './tiles.ts'

function family(sites: DomainSources['sites'], domain: string): Family {
  return buildFamilies({ sites, zones: [], redirects: [] }).find((f) => f.domain === domain)!
}

describe('main site of a domain', () => {
  it('prefers the live site on the domain itself or its www address', () => {
    const f = family(
      [
        { name: 'a-blog', domains: ['blog.example.com'], live: true, release: 'r1' },
        { name: 'b-home', domains: ['www.example.com', 'example.com'], live: true, release: 'r2' },
        { name: 'c-old', domains: ['example.com'], live: false },
      ],
      'example.com',
    )
    expect(mainSite(f)?.name).toBe('b-home')
  })

  it('takes the first live site when none is on the domain itself', () => {
    const f = family(
      [
        { name: 'apex', domains: ['example.com'], live: false },
        { name: 'docs', domains: ['docs.example.com'], live: true, release: 'r1' },
        { name: 'shop', domains: ['shop.example.com'], live: true, release: 'r2' },
      ],
      'example.com',
    )
    expect(mainSite(f)?.name).toBe('docs')
  })

  it('has none without a live site', () => {
    expect(mainSite(family([{ name: 'draft', domains: ['example.com'], live: false }], 'example.com'))).toBeNull()
  })

  it('loads the icon of the live version', () => {
    const f = family([{ name: 'my site', domains: ['example.com'], live: true, release: '2026-01' }], 'example.com')
    expect(iconUrl(mainSite(f)!)).toBe('/api/sites/my%20site/icon?v=2026-01')
  })
})

describe('tile menu actions', () => {
  afterEach(() => vi.unstubAllGlobals())

  const site = family(
    [{ name: 'shop', domains: ['shop.example.com', 'www.example.com'], live: true, release: 'r1' }],
    'example.com',
  )

  it('opens the first domain of the main site in a new tab', () => {
    const open = vi.fn()
    vi.stubGlobal('window', { open })
    expect(siteAddress(site)).toBe('https://shop.example.com/')
    expect(openSite(site)).toBe(true)
    expect(open).toHaveBeenCalledWith('https://shop.example.com/', '_blank', 'noopener,noreferrer')
  })

  it('opens nothing without a live site', () => {
    const open = vi.fn()
    vi.stubGlobal('window', { open })
    const draft = family([{ name: 'draft', domains: ['draft.example.com'], live: false }], 'example.com')
    expect(openSite(draft)).toBe(false)
    expect(open).not.toHaveBeenCalled()
  })

  it('copies the domain', async () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    await copyDomain(site)
    expect(writeText).toHaveBeenCalledWith('example.com')
  })
})
