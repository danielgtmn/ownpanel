import { Dialog } from '@ui-sdk/react/dialog'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createZone, deleteZone, recordPath, recordsPath, deleteRecord, listRecords, listZones, saveRecord } from './api.ts'
import { AddDomainForm } from './AddDomainDialog.tsx'
import { DeleteDomainForm } from './DeleteDomainDialog.tsx'
import { Nameservers } from './Nameservers.tsx'
import type { ZoneList } from './types.ts'
import {
  canConfirmDelete,
  describeZones,
  findZone,
  nameserverList,
  parseConnectionId,
  problemMessage,
  usesFromMessage,
  validateZoneName,
} from './zones.ts'

const list: ZoneList = {
  zones: [
    { connection_id: 2, connection_name: 'B', name: 'zeta.org', read_only: false, nameservers: [], adopted: true, used_by: [] },
    { connection_id: 1, connection_name: 'A', name: 'alpha.com', read_only: true, nameservers: [], adopted: false, used_by: ['the site blog (alpha.com)'] },
  ],
  problems: [{ connection_id: 3, connection_name: 'Old', error: 'The API token was rejected.' }],
}

describe('zone list', () => {
  it('sorts domains and turns problems into notices', () => {
    const view = describeZones(list)
    expect(view.rows.map((row) => row.name)).toEqual(['alpha.com', 'zeta.org'])
    expect(view.rows[0]).toMatchObject({ connectionId: 1, readOnly: true, usedBy: ['the site blog (alpha.com)'] })
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

describe('adding and deleting domains', () => {
  it('enables Delete only when the typed name matches', () => {
    expect(canConfirmDelete('', 'example.com')).toBe(false)
    expect(canConfirmDelete('example.co', 'example.com')).toBe(false)
    expect(canConfirmDelete('www.example.com', 'example.com')).toBe(false)
    expect(canConfirmDelete('example.com', 'example.com')).toBe(true)
    expect(canConfirmDelete(' Example.COM. ', 'example.com')).toBe(true)
  })

  it('renders the Delete button disabled until the name is typed', () => {
    const html = renderToStaticMarkup(
      <Dialog>
        <DeleteDomainForm zone="example.com" onDelete={async () => undefined} />
      </Dialog>,
    )
    expect(html).toContain('Type example.com to confirm')
    expect(html).toMatch(/<button[^>]*disabled[^>]*>Delete domain<\/button>/)
  })

  it('lists what still uses a domain', () => {
    const message =
      'The domain example.com is still in use by the site blog (example.com, www.example.com), the redirect of go.example.com, a site of another person (shop.example.com), the panel itself (panel.example.com); remove those first'
    expect(usesFromMessage(message)).toEqual([
      'The site blog (example.com, www.example.com)',
      'The redirect of go.example.com',
      'A site of another person (shop.example.com)',
      'The panel itself (panel.example.com)',
    ])
    expect(usesFromMessage('Not deleted: type the domain name example.com exactly to confirm')).toEqual([])
  })

  it('shows nameservers without trailing dots or repeats', () => {
    expect(nameserverList(['hydrogen.ns.hetzner.com.', 'oxygen.ns.hetzner.com', 'Hydrogen.ns.hetzner.com', ' '])).toEqual([
      'hydrogen.ns.hetzner.com',
      'oxygen.ns.hetzner.com',
    ])
    expect(nameserverList(null)).toEqual([])
  })

  it('renders each nameserver with a copy button', () => {
    const html = renderToStaticMarkup(<Nameservers nameservers={['a.ns.example.net.', 'b.ns.example.net']} domain="example.com" />)
    expect(html).toContain('<code>a.ns.example.net</code>')
    expect(html).toContain('Copy<span class="visually-hidden"> b.ns.example.net</span>')
    expect(renderToStaticMarkup(<Nameservers nameservers={[]} domain="example.com" />)).toContain('didn&#x27;t report')
  })

  it('checks the domain name before asking the provider', () => {
    expect(validateZoneName('example.com')).toBeNull()
    expect(validateZoneName('Example.co.uk.')).toBeNull()
    expect(validateZoneName('')).toMatch(/enter the domain/i)
    expect(validateZoneName('https://example.com')).toMatch(/without https/)
    expect(validateZoneName('203.0.113.5')).toMatch(/IP address/)
    expect(validateZoneName('bücher.de')).toMatch(/xn--/)
    expect(validateZoneName('example')).toMatch(/isn't a valid domain/)
  })

  it('creates and deletes a domain through the connection', async () => {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) =>
      init?.method === 'DELETE'
        ? new Response(null, { status: 204 })
        : new Response(
            JSON.stringify({ name: 'example.com', connection_id: 4, connection_name: 'Main', read_only: false, nameservers: null }),
            { status: 201, headers: { 'Content-Type': 'application/json' } },
          ),
    )
    vi.stubGlobal('fetch', fetchMock)
    const zone = await createZone(4, 'example.com')
    expect(zone.nameservers).toEqual([])
    await deleteZone(4, 'example.com', 'example.com')
    expect(fetchMock.mock.calls.map(([url, init]) => `${init?.method} ${url} ${String(init?.body)}`)).toEqual([
      'POST /api/dns/connections/4/zones {"name":"example.com"}',
      'DELETE /api/dns/connections/4/zones/example.com {"confirm":"example.com"}',
    ])
  })

  it('shows the provider’s refusal as the reason', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ error: 'The domain example.com is still in use by the site blog (example.com); remove those first' }), { status: 409 })),
    )
    const error = await deleteZone(4, 'example.com', 'example.com').catch((caught: unknown) => caught)
    expect(usesFromMessage((error as Error).message)).toEqual(['The site blog (example.com)'])
  })
})

describe('add domain form', () => {
  const account = (id: number, name: string) => ({
    id,
    provider: 'hetzner' as const,
    name,
    owner: 'ana',
    created_at: '2026-01-01T00:00:00Z',
    last_checked_at: null,
    last_error: '',
    can_write: true,
    zone_count: 1,
  })
  const zone = (connection_id: number, connection_name: string, name: string) => ({
    connection_id,
    connection_name,
    name,
    read_only: false,
    nameservers: [],
    adopted: false,
    used_by: [],
  })
  const render = (props: Partial<Parameters<typeof AddDomainForm>[0]>) =>
    renderToStaticMarkup(
      <Dialog>
        <AddDomainForm
          accounts={[account(1, 'Home')]}
          available={{ zones: [], problems: [] }}
          onAdopt={async () => undefined}
          onCreate={async () => undefined}
          {...props}
        />
      </Dialog>,
    )

  it('starts with choosing when the accounts have domains that aren’t managed here', () => {
    const html = render({ available: { zones: [zone(1, 'Home', 'example.com')], problems: [] } })
    expect(html).toContain('Choose one from your accounts')
    expect(html).toContain("Domains in Home that aren&#x27;t managed here yet")
    expect(html).not.toContain('New domain')
  })

  it('offers only creating when every domain is managed here already', () => {
    const html = render({})
    expect(html).toContain('Every domain in your accounts is managed here already.')
    expect(html).toContain('New domain')
    expect(html).toContain('created in Home')
  })

  it('creates a prefilled domain that no account has, and lets you pick the account', () => {
    const html = render({
      accounts: [account(1, 'Home'), account(2, 'Work')],
      available: { zones: [zone(1, 'Home', 'other.org')], problems: [] },
      domain: 'fresh.com',
    })
    expect(html).toContain('value="fresh.com"')
    expect(html).toContain('Account')
  })
})
