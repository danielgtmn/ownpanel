import { afterEach, describe, expect, it, vi } from 'vitest'
import { recordPath, recordsPath, deleteRecord, listRecords, listZones, saveRecord } from './api.ts'
import type { Connection, ZoneList } from './types.ts'
import {
  connectionHealth,
  describeZones,
  findZone,
  isOwnConnection,
  parseConnectionId,
  problemMessage,
  validateConnection,
  zoneCountLabel,
} from './zones.ts'

const list: ZoneList = {
  zones: [
    { connection_id: 2, connection_name: 'B', name: 'zeta.org', read_only: false },
    { connection_id: 1, connection_name: 'A', name: 'alpha.com', read_only: true },
  ],
  problems: [{ connection_id: 3, connection_name: 'Old', error: 'The API token was rejected.' }],
}

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

describe('zone list', () => {
  it('sorts domains and turns problems into notices', () => {
    const view = describeZones(list)
    expect(view.rows.map((row) => row.name)).toEqual(['alpha.com', 'zeta.org'])
    expect(view.rows[0]).toMatchObject({ connectionId: 1, readOnly: true })
    expect(view.notices).toEqual([
      { key: 3, text: "Couldn't load the domains from Old. The API token was rejected." },
    ])
  })

  it('still names the connection when the reason is empty', () => {
    expect(problemMessage({ connection_id: 1, connection_name: 'X', error: '  ' })).toBe(
      "Couldn't load the domains from X.",
    )
  })

  it('finds a domain by connection and name', () => {
    expect(findZone(list, 1, 'alpha.com')?.read_only).toBe(true)
    expect(findZone(list, 2, 'alpha.com')).toBeUndefined()
  })

  it('reads the connection id from the address', () => {
    expect(parseConnectionId('12')).toBe(12)
    expect(parseConnectionId('0')).toBeNull()
    expect(parseConnectionId('x')).toBeNull()
    expect(parseConnectionId(undefined)).toBeNull()
  })
})

describe('connections', () => {
  it('marks other people connections for admins only', () => {
    const theirs = connection({ owner: 'bo' })
    expect(isOwnConnection(theirs, { name: 'ana', role: 'admin' })).toBe(false)
    expect(isOwnConnection(connection(), { name: 'ana', role: 'admin' })).toBe(true)
    expect(isOwnConnection(theirs, { name: 'ana', role: 'member' })).toBe(true)
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
      name: 'Enter a name to recognize this connection.',
      token: 'Paste the API token from Hetzner.',
    })
    expect(validateConnection('Main', 'abc')).toEqual({})
  })
})

describe('record requests', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

  it('encodes every path segment', () => {
    expect(recordsPath(4, 'example.com')).toBe('/dns/connections/4/zones/example.com/records')
    expect(recordPath(4, 'example.com', '@', 'A')).toBe('/dns/connections/4/zones/example.com/records/%40/A')
    expect(recordPath(4, 'example.com', '*', 'A')).toBe('/dns/connections/4/zones/example.com/records/%2A/A')
    expect(recordPath(4, 'example.com', '*.dev', 'A')).toContain('/%2A.dev/')
    expect(recordPath(4, 'example.com', '_dmarc', 'TXT')).toBe(
      '/dns/connections/4/zones/example.com/records/_dmarc/TXT',
    )
    expect(recordPath(4, 'a/b', 'x y', 'A')).toBe('/dns/connections/4/zones/a%2Fb/records/x%20y/A')
  })

  it('sends PUT with ttl and values and the change header', async () => {
    const record = { name: '@', type: 'A', ttl: null, values: ['1.2.3.4'], locked: false }
    const fetchMock = vi.fn(async () => json(record))
    vi.stubGlobal('fetch', fetchMock)
    await expect(saveRecord(4, 'example.com', '@', 'A', { ttl: null, values: ['1.2.3.4'] })).resolves.toEqual(record)
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/dns/connections/4/zones/example.com/records/%40/A')
    expect(init.method).toBe('PUT')
    expect(init.body).toBe('{"ttl":null,"values":["1.2.3.4"]}')
    expect((init.headers as Record<string, string>)['X-Ownpanel-Request']).toBe('1')
  })

  it('sends DELETE and accepts an empty answer', async () => {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(deleteRecord(4, 'example.com', '_dmarc', 'TXT')).resolves.toBeUndefined()
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/dns/connections/4/zones/example.com/records/_dmarc/TXT')
    expect(init.method).toBe('DELETE')
  })

  it('tolerates empty lists from the server', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(null)))
    await expect(listRecords(1, 'example.com')).resolves.toEqual([])
    vi.stubGlobal('fetch', vi.fn(async () => json({ zones: null, problems: null })))
    await expect(listZones()).resolves.toEqual({ zones: [], problems: [] })
  })

  it('surfaces the server message', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'Hetzner is busy. Try again soon.' }, 429)))
    await expect(listRecords(1, 'example.com')).rejects.toMatchObject({
      status: 429,
      message: 'Hetzner is busy. Try again soon.',
    })
  })
})
