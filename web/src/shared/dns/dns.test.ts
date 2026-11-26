import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  addDomain,
  adoptZone,
  createZone,
  listAddedDomains,
  listAvailableZones,
  listZones,
  releaseZone,
  removeAddedDomain,
} from './api.ts'
import { connectionHealth, isOwnConnection, ownConnections, validateConnection, zoneCountLabel } from './connections.ts'
import type { Connection, Zone } from './types.ts'
import { groupByAccount, staysBecause, zoneKey, zonesNamed } from './zones.ts'

function connection(overrides: Partial<Connection> = {}): Connection {
  return {
    id: 1,
    provider: 'hetzner',
    name: 'Main',
    owner: 'ana',
    created_at: '2026-01-01T00:00:00Z',
    last_checked_at: null,
    last_error: '',
    can_write: true,
    zone_count: 2,
    ...overrides,
  }
}

function zone(connection_id: number, connection_name: string, name: string, overrides: Partial<Zone> = {}): Zone {
  return { connection_id, connection_name, name, read_only: false, nameservers: [], adopted: false, used_by: [], ...overrides }
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

describe('connected accounts', () => {
  it('marks other people’s accounts for admins only', () => {
    const theirs = connection({ owner: 'bo' })
    expect(isOwnConnection(theirs, { name: 'ana', role: 'admin' })).toBe(false)
    expect(isOwnConnection(connection(), { name: 'ana', role: 'admin' })).toBe(true)
    expect(isOwnConnection(theirs, { name: 'ana', role: 'member' })).toBe(true)
    expect(ownConnections([theirs, connection()], { name: 'ana', role: 'admin' })).toEqual([connection()])
  })

  it('summarizes health', () => {
    const format = (value: string) => `on ${value}`
    expect(connectionHealth(connection({ last_error: 'Token rejected' }), format)).toEqual({
      tone: 'problem',
      text: 'Problem: Token rejected',
    })
    expect(connectionHealth(connection({ last_checked_at: '2026-02-02' }), format).tone).toBe('ok')
    expect(connectionHealth(connection(), format)).toEqual({ tone: 'unknown', text: 'Not checked yet' })
  })

  it('labels the number of domains', () => {
    expect(zoneCountLabel(null)).toBeNull()
    expect(zoneCountLabel(1)).toBe('1 domain')
    expect(zoneCountLabel(3)).toBe('3 domains')
  })

  it('validates the connect form', () => {
    expect(validateConnection('', '')).toEqual({
      name: 'Enter a name to recognize this account.',
      token: 'Paste the API token from Hetzner.',
    })
    expect(validateConnection('Main', 'abc')).toEqual({})
  })
})

describe('choosing domains', () => {
  it('groups the domains to choose from by account, both by name', () => {
    const groups = groupByAccount([zone(2, 'Work', 'b.org'), zone(1, 'Home', 'z.com'), zone(2, 'Work', 'a.org')])
    expect(groups.map((group) => [group.connectionName, group.zones.map((z) => z.name)])).toEqual([
      ['Home', ['z.com']],
      ['Work', ['a.org', 'b.org']],
    ])
    expect(zoneKey(groups[1].zones[0])).toBe('2/a.org')
  })

  it('finds the zones of a domain', () => {
    const zones = [zone(1, 'A', 'example.com'), zone(2, 'B', 'example.com'), zone(1, 'A', 'other.org')]
    expect(zonesNamed(zones, 'Example.COM.').map((z) => z.connection_id)).toEqual([1, 2])
    expect(zonesNamed(zones, 'nope.net')).toEqual([])
  })

  it('says why a used domain stays', () => {
    expect(staysBecause(zone(1, 'A', 'example.com'))).toBeNull()
    expect(staysBecause(zone(1, 'A', 'example.com', { used_by: ['the site blog (blog.example.com)', 'e-mail for example.com'] }))).toBe(
      'example.com stays here while it is used by the site blog (blog.example.com), e-mail for example.com.',
    )
  })
})

describe('domain requests', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('lists managed and available domains, tolerating missing fields', async () => {
    const fetchMock = vi.fn(async (url: string) =>
      url.endsWith('/available')
        ? json({ zones: [{ connection_id: 1, connection_name: 'A', name: 'x.org', read_only: false }], problems: null })
        : json({ zones: null }),
    )
    vi.stubGlobal('fetch', fetchMock)
    await expect(listZones()).resolves.toEqual({ zones: [], problems: [] })
    await expect(listAvailableZones()).resolves.toEqual({
      zones: [zone(1, 'A', 'x.org')],
      problems: [],
    })
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual(['/api/dns/zones', '/api/dns/zones/available'])
  })

  it('adopts, releases and creates domains', async () => {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) =>
      init?.method === 'DELETE'
        ? new Response(null, { status: 204 })
        : json({ connection_id: 4, connection_name: 'Main', name: 'example.com', read_only: false, nameservers: null, adopted: true, used_by: null }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const adopted = await adoptZone(4, 'example.com')
    expect(adopted).toMatchObject({ adopted: true, used_by: [], nameservers: [] })
    await releaseZone(4, 'example.com')
    await createZone(4, 'example.com')
    expect(fetchMock.mock.calls.map(([url, init]) => `${init?.method} ${url}`)).toEqual([
      'PUT /api/dns/connections/4/zones/example.com/managed',
      'DELETE /api/dns/connections/4/zones/example.com/managed',
      'POST /api/dns/connections/4/zones',
    ])
  })

  it('adds, lists and removes domains whose DNS is managed elsewhere', async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') return json({ name: 'example.com', used_by: ['the site blog (blog.example.com)'] })
      if (init?.method === 'POST') return json({ name: 'example.com', created_at: '2026-01-01T00:00:00Z', used_by: null })
      return url === '/api/domains' ? json(null) : json({ error: 'not found' }, 404)
    })
    vi.stubGlobal('fetch', fetchMock)
    await expect(listAddedDomains()).resolves.toEqual([])
    await expect(addDomain('example.com')).resolves.toEqual({
      name: 'example.com',
      created_at: '2026-01-01T00:00:00Z',
      used_by: [],
    })
    const removed = await removeAddedDomain('example.com')
    expect(staysBecause(removed)).toBe('example.com stays here while it is used by the site blog (blog.example.com).')
    expect(fetchMock.mock.calls.map(([url, init]) => `${init?.method ?? 'GET'} ${url}`)).toEqual([
      'GET /api/domains',
      'POST /api/domains',
      'DELETE /api/domains/example.com',
    ])
    expect(fetchMock.mock.calls[1][1]?.body).toBe('{"name":"example.com"}')
  })
})
