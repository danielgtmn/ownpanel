import { afterEach, describe, expect, it, vi } from 'vitest'
import { getDomainStatuses, getServerAddresses, pointDomain, type DomainStatus } from '../api/index.ts'
import { foundText, pointedMessage, statusAction, statusText, statusTone } from './status.ts'

const zone = { connection_id: 3, connection_name: 'Main', zone: 'example.com', record_name: '@' }

function status(state: DomainStatus['state'], overrides: Partial<DomainStatus> = {}): DomainStatus {
  return { domain: 'example.com', state, found: [], zone, ...overrides }
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('statusText', () => {
  it('describes every state', () => {
    expect(statusText(status('points_here'))).toBe('Points to this server')
    expect(statusText(status('points_elsewhere', { found: ['1.2.3.4', '5.6.7.8'] }))).toBe('Points somewhere else')
    expect(foundText(status('points_elsewhere', { found: ['1.2.3.4', '5.6.7.8'] }))).toBe(
      'Right now it points to 1.2.3.4, 5.6.7.8.',
    )
    expect(statusText(status('points_elsewhere'))).toBe('Points somewhere else')
    expect(statusText(status('no_record'))).toBe('No address record yet')
    expect(statusText(status('lookup_failed'))).toBe('Could not be checked')
  })

  it('picks a tone', () => {
    expect(statusTone(status('points_here'))).toBe('positive')
    expect(statusTone(status('no_record'))).toBe('warning')
    expect(statusTone(status('points_elsewhere'))).toBe('warning')
    expect(statusTone(status('lookup_failed'))).toBe('neutral')
  })
})

describe('statusAction', () => {
  it('offers nothing when the domain already points here', () => {
    expect(statusAction(status('points_here'))).toBe('none')
    expect(statusAction(status('points_here', { zone: null }))).toBe('none')
  })

  it('offers the one-click fix when a connection manages the domain', () => {
    for (const state of ['points_elsewhere', 'no_record', 'lookup_failed'] as const) {
      expect(statusAction(status(state))).toBe('point')
    }
  })

  it('shows the manual help when no connection manages the domain', () => {
    expect(statusAction(status('no_record', { zone: null }))).toBe('manual')
    expect(statusAction(status('no_record'))).toBe('point')
  })
})

describe('pointedMessage', () => {
  it('names what changed and warns about delays', () => {
    const message = pointedMessage(
      'www.example.com',
      [{ name: 'www', type: 'A', ttl: null, values: ['203.0.113.5'], locked: false }],
      'example.com',
    )
    expect(message).toContain('A www.example.com')
    expect(message).toContain('203.0.113.5')
    expect(message).toMatch(/few minutes/)
  })

  it('shows the apex as the domain itself', () => {
    const message = pointedMessage(
      'example.com',
      [{ name: '@', type: 'AAAA', ttl: 300, values: ['2001:db8::1'], locked: false }],
      'example.com',
    )
    expect(message).toContain('AAAA example.com → 2001:db8::1')
  })

  it('says so when nothing had to change', () => {
    expect(pointedMessage('example.com', [], 'example.com')).toBe('example.com already points to this server.')
  })
})

describe('api calls', () => {
  it('encodes the site and domain', async () => {
    const fetchMock = vi.fn(async () => json([]))
    vi.stubGlobal('fetch', fetchMock)
    await getDomainStatuses('my site')
    expect(fetchMock.mock.calls[0]).toEqual([
      '/api/sites/my%20site/domains/status',
      expect.objectContaining({ method: 'GET' }),
    ])
  })

  it('posts to the point endpoint and returns the changes', async () => {
    const record = { name: '@', type: 'A', ttl: null, values: ['1.2.3.4'], locked: false }
    const fetchMock = vi.fn(async () => json({ changed: [record] }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(pointDomain('a', '*.example.com')).resolves.toEqual([record])
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/sites/a/domains/%2A.example.com/point')
    expect((init.headers as Record<string, string>)['X-Ownpanel-Request']).toBe('1')
  })

  it('treats an empty change list and empty address list as empty', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ changed: null, ips: null })))
    await expect(pointDomain('a', 'example.com')).resolves.toEqual([])
    await expect(getServerAddresses()).resolves.toEqual([])
  })
})
