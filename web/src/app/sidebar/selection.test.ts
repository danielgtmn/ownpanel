import { afterEach, describe, expect, it, vi } from 'vitest'
import { modules } from '../../modules/registry.ts'
import { buildFamilies, familySummary, type DomainSources, type Family } from '../../shared/families/index.ts'
import { parseRoute } from '../../shared/routing/index.ts'
import type { User } from '../../shared/session/index.ts'
import { visibleModules } from '../modules.ts'
import { loadPointStates, loadSources } from './data.ts'
import { ALL, currentRow, familyRows, overviewRows, railEntries, selectTile } from './selection.ts'

const admin: User = { id: 1, name: 'anna', email: '', role: 'admin', created_at: '2026-01-01T00:00:00Z' }
const member: User = { id: 2, name: 'max', email: '', role: 'member', created_at: '2026-01-01T00:00:00Z' }

const sources: DomainSources = {
  sites: [
    { name: 'blog', domains: ['blog.example.com', 'www.example.com'], live: true },
    { name: 'shop', domains: ['shop.de', 'shop.example.com'], live: false },
    { name: 'draft', domains: [], live: false },
  ],
  zones: [{ connectionId: 3, connectionName: 'Hetzner', name: 'example.com' }],
  redirects: [{ id: 1, source: 'go.shop.de', target: 'https://shop.de' }],
}
const families = buildFamilies(sources)
const available = visibleModules(modules, admin)
const rowsOf = (family: Family) => familyRows(available, family)
const overview = overviewRows(available, (module) => module.overviewRow?.count(sources) ?? null)

function tileFor(hash: string, previous = ALL) {
  return selectTile(parseRoute(hash), families, rowsOf, overview, previous)
}

describe('route to selection', () => {
  it('opens the family a family address names', () => {
    expect(tileFor('#/d/example.com')).toBe('example.com')
    expect(tileFor('#/d/Shop.DE')).toBe('shop.de')
  })

  it('selects the family of a site opened by address', () => {
    expect(tileFor('#/sites/blog')).toBe('example.com')
    expect(tileFor('#/sites/blog/versions')).toBe('example.com')
  })

  it('keeps the open family when a site belongs to several', () => {
    expect(tileFor('#/sites/shop')).toBe('example.com')
    expect(tileFor('#/sites/shop', 'shop.de')).toBe('shop.de')
  })

  it('selects the family of a DNS zone and of filtered redirects', () => {
    expect(tileFor('#/dns/3/example.com')).toBe('example.com')
    expect(tileFor('#/redirects?family=shop.de')).toBe('shop.de')
  })

  it('selects "All" for overview pages and sites without domains', () => {
    expect(tileFor('#/sites', 'example.com')).toBe(ALL)
    expect(tileFor('#/redirects', 'example.com')).toBe(ALL)
    expect(tileFor('#/sites/draft', 'shop.de')).toBe(ALL)
  })

  it('keeps the open family for pages no column lists', () => {
    expect(tileFor('#/settings', 'shop.de')).toBe('shop.de')
    expect(tileFor('#/tokens')).toBe(ALL)
  })

  it('marks the row of the current page', () => {
    const example = families.find((family) => family.domain === 'example.com')!
    const rows = rowsOf(example)
    expect(rows.map((row) => row.label)).toEqual(['Overview', 'blog', 'shop', 'DNS records', 'Redirects'])
    expect(currentRow(rows, parseRoute('#/sites/blog'))).toBe('site:blog')
    expect(currentRow(rows, parseRoute('#/d/example.com'))).toBe('family-overview')
    expect(currentRow(rows, parseRoute('#/redirects?family=example.com'))).toBe('redirects')
    expect(currentRow(rows, parseRoute('#/redirects'))).toBeNull()
    expect(rows.find((row) => row.key === 'site:shop')?.hint).toBe('Website, also on shop.de')
    expect(rows.find((row) => row.key === 'site:shop')?.status).toBe('attention')
  })

  it('links each family row to the existing pages', () => {
    const shop = families.find((family) => family.domain === 'shop.de')!
    expect(Object.fromEntries(rowsOf(shop).map((row) => [row.key, row.href]))).toEqual({
      'family-overview': '#/d/shop.de',
      'site:shop': '#/sites/shop',
      redirects: '#/redirects?family=shop.de',
    })
  })

  it('counts the overview rows', () => {
    expect(overview.map((row) => [row.label, row.detail])).toEqual([
      ['Sites', '3'],
      ['DNS', '1'],
      ['Redirects', '1'],
      ['E-mail', '0'],
    ])
  })
})

describe('rail entries', () => {
  it('shows administrators every entry', () => {
    expect(railEntries(visibleModules(modules, admin), null, null).map((entry) => entry.label)).toEqual([
      'DNS connections',
      'Settings',
      'People',
      'Activity',
      'Access tokens',
    ])
  })

  it('hides administrator-only entries from members', () => {
    expect(railEntries(visibleModules(modules, member), null, null).map((entry) => entry.label)).toEqual([
      'DNS connections',
      'Access tokens',
    ])
  })

  it('marks an entry current only when no column row does', () => {
    const entries = (rowKey: string | null) => railEntries(available, 'settings', rowKey)
    expect(entries(null).find((entry) => entry.key === 'settings')?.current).toBe(true)
    expect(entries('overview:dns').every((entry) => !entry.current)).toBe(true)
  })
})

describe('loadSources', () => {
  afterEach(() => vi.unstubAllGlobals())

  function stubFetch() {
    const fetchMock = vi.fn(async (url: string) => {
      const bodies: Record<string, unknown> = {
        '/api/sites': [{ name: 'blog', domains: ['blog.example.com'], current_release: '20260101-000000' }],
        '/api/dns/zones': { zones: [{ connection_id: 1, connection_name: 'H', name: 'example.com' }], problems: [] },
        '/api/redirects': [
          { id: 1, source_domain: 'go.example.com', source_path: '', target: 'https://x.org' },
          { id: 2, source_domain: 'blog.example.com', source_path: '/old', target: '/new' },
        ],
        '/api/settings': { panel_domain: 'panel.example.com' },
        '/api/settings/checks': { dns: { state: 'points_here' } },
      }
      return new Response(JSON.stringify(bodies[url] ?? null), { status: url in bodies ? 200 : 404 })
    })
    vi.stubGlobal('fetch', fetchMock)
    return fetchMock
  }

  it('reads sites, DNS domains and domain redirects', async () => {
    const fetchMock = stubFetch()
    const result = await loadSources()
    expect(result).toEqual({
      sites: [{ name: 'blog', domains: ['blog.example.com'], live: true, release: '20260101-000000' }],
      zones: [{ connectionId: 1, connectionName: 'H', name: 'example.com' }],
      redirects: [{ id: 1, source: 'go.example.com', target: 'https://x.org' }],
      pathRedirectHosts: ['blog.example.com'],
    })
    expect(fetchMock.mock.calls.map(([url]) => url)).not.toContain('/api/settings')
  })

  it('leaves the panel address out: no tile, no lookups, no effect on the status', async () => {
    const fetchMock = stubFetch()
    const sources = await loadSources()
    const states = await loadPointStates(sources)
    const urls = fetchMock.mock.calls.map(([url]) => url)
    expect(urls.filter((url) => url.startsWith('/api/settings'))).toEqual([])
    const families = buildFamilies(sources)
    expect(families.map((family) => family.domain)).toEqual(['example.com'])
    expect(families[0].servedDomains).not.toContain('panel.example.com')
    expect(Object.keys(states)).not.toContain('panel.example.com')
    expect(familySummary(families[0])).not.toContain('Panel')
  })

  it('still shows sites when DNS domains cannot be loaded', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url === '/api/dns/zones'
          ? new Response('{"error":"down"}', { status: 502 })
          : new Response(url === '/api/sites' ? '[]' : 'null', { status: 200 }),
      ),
    )
    await expect(loadSources()).resolves.toEqual({ sites: [], zones: [], redirects: [], pathRedirectHosts: [] })
  })
})
