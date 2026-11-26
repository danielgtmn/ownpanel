import { afterEach, describe, expect, it, vi } from 'vitest'
import { collectAddresses } from '../../shared/families/index.ts'
import { loadSources, loadZoneRecords } from './data.ts'

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function mockRoutes(routes: Record<string, () => Response>) {
  const fn = vi.fn(async (url: string) => {
    const route = routes[url]
    if (!route) return json({ error: 'not found' }, 404)
    return route()
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('loading the addresses of a domain', () => {
  it('keeps the domains of path redirects apart from domain redirects', async () => {
    mockRoutes({
      '/api/sites': () => json([{ name: 'main', domains: ['example.com'], current_release: '' }]),
      '/api/dns/zones': () => json({ zones: [] }),
      '/api/redirects': () =>
        json([
          { id: 1, source_domain: 'go.example.com', source_path: '', target: 'https://example.org' },
          { id: 2, source_domain: 'docs.example.com', source_path: '/old', target: '/new' },
          { id: 3, source_domain: 'docs.example.com', source_path: '/a/*', target: '/b' },
        ]),
    })
    const sources = await loadSources()
    expect(sources.redirects).toEqual([{ id: 1, source: 'go.example.com', target: 'https://example.org' }])
    expect(sources.pathRedirectHosts).toEqual(['docs.example.com'])
  })

  it('loads the mail domains, and none when e-mail may not be seen', async () => {
    const base = {
      '/api/sites': () => json([]),
      '/api/dns/zones': () => json({ zones: [] }),
      '/api/redirects': () => json([]),
    }
    mockRoutes({
      ...base,
      '/api/mail/domains': () => json([{ domain: 'example.org', active: false, owner: 'anna', mailboxes: 1 }]),
    })
    expect((await loadSources()).mailDomains).toEqual([{ domain: 'example.org', active: false }])
    mockRoutes({ ...base, '/api/mail/domains': () => json({ error: 'limited to sites' }, 403) })
    expect((await loadSources()).mailDomains).toBeUndefined()
  })

  it('loads the domains added by hand, and none when they may not be seen', async () => {
    const base = {
      '/api/sites': () => json([]),
      '/api/dns/zones': () => json({ zones: [] }),
      '/api/redirects': () => json([]),
    }
    mockRoutes({
      ...base,
      '/api/domains': () => json([{ name: 'elsewhere.org', created_at: '2026-01-01T00:00:00Z', used_by: [] }]),
    })
    expect((await loadSources()).domains).toEqual(['elsewhere.org'])
    mockRoutes({ ...base, '/api/domains': () => json({ error: 'limited to sites' }, 403) })
    expect((await loadSources()).domains).toBeUndefined()
  })

  it('reads the record names of each DNS account and reports the ones that failed', async () => {
    const fetchMock = mockRoutes({
      '/api/dns/connections/1/zones/example.com/records': () =>
        json([
          { name: 'shop', type: 'A', ttl: null, values: ['203.0.113.5'], locked: false },
          { name: '_dmarc', type: 'TXT', ttl: null, values: ['v=DMARC1'], locked: false },
        ]),
      '/api/dns/connections/2/zones/example.com/records': () => json({ error: 'token expired' }, 502),
    })
    const result = await loadZoneRecords([
      { connectionId: 1, connectionName: 'Hetzner', name: 'example.com' },
      { connectionId: 2, connectionName: 'Old', name: 'example.com' },
    ])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(result.failed).toEqual(['example.com'])
    expect(result.zones).toEqual([
      {
        zone: 'example.com',
        records: [
          { name: 'shop', type: 'A' },
          { name: '_dmarc', type: 'TXT' },
        ],
      },
    ])
    const addresses = collectAddresses('example.com', { sites: [], zones: [], redirects: [] }, result.zones)
    expect(addresses.map((address) => address.host)).toEqual(['example.com', 'www.example.com', 'shop.example.com'])
  })

  it('asks nothing when no DNS account holds the domain', async () => {
    const fetchMock = mockRoutes({})
    expect(await loadZoneRecords([])).toEqual({ zones: [], failed: [] })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
