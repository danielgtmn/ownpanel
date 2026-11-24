import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRedirect, deleteRedirect, listSiteRedirects, updateRedirect, type Redirect } from '../api/index.ts'
import {
  codeFor,
  codeLabel,
  domainRedirects,
  draftFrom,
  inputFrom,
  keepsSummary,
  routeRedirectError,
  sortRedirects,
  validateRedirect,
  validateSourceDomain,
  validateSourcePath,
  type RedirectDraft,
} from './rules.ts'

const draft = (overrides: Partial<RedirectDraft> = {}): RedirectDraft => ({
  domain: 'example.com',
  path: '/old',
  target: '/new',
  permanent: true,
  keepMethod: false,
  keepPath: false,
  keepQuery: false,
  ...overrides,
})

function redirect(id: number, domain: string, path: string): Redirect {
  return {
    id,
    owner: 'anna',
    source_domain: domain,
    source_path: path,
    target: 'https://example.org',
    code: 301,
    keep_path: true,
    keep_query: false,
    created_at: '2026-01-01T00:00:00Z',
  }
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('validateSourcePath', () => {
  it('accepts exact paths and prefixes', () => {
    for (const path of ['/old', '/blog/*', '/*', '/über/seite', '/a.html']) {
      expect(validateSourcePath(path)).toBeNull()
    }
  })

  it('refuses what the server refuses', () => {
    expect(validateSourcePath('')).toMatch(/enter the path/i)
    expect(validateSourcePath('old')).toMatch(/must start with \//)
    expect(validateSourcePath('/a b')).toMatch(/spaces/)
    expect(validateSourcePath('/a{b}')).toMatch(/braces/)
    expect(validateSourcePath('/a?x=1')).toMatch(/\? or #/)
    expect(validateSourcePath('/a#top')).toMatch(/\? or #/)
    expect(validateSourcePath('/a%20b')).toMatch(/percent/)
    expect(validateSourcePath('/a*/b')).toMatch(/only end in \/\*/)
    expect(validateSourcePath('/blog*')).toMatch(/only end in \/\*/)
    expect(validateSourcePath('/a/../b')).toMatch(/\. or \.\./)
    expect(validateSourcePath('/a//b')).toMatch(/\/\//)
    expect(validateSourcePath('/' + 'a'.repeat(1024))).toMatch(/at most 1024/)
  })
})

describe('validateSourceDomain', () => {
  it('accepts domain names and refuses addresses and paths', () => {
    expect(validateSourceDomain('Old.Example.com.')).toBeNull()
    expect(validateSourceDomain('')).toMatch(/enter the domain/i)
    expect(validateSourceDomain('203.0.113.5')).toMatch(/not an IP address/)
    expect(validateSourceDomain('[2001:db8::1]')).toMatch(/not an IP address/)
    expect(validateSourceDomain('https://example.com')).toMatch(/without https:\/\//)
    expect(validateSourceDomain('example.com/a')).toMatch(/without a path/)
    expect(validateSourceDomain('localhost')).toMatch(/isn't a valid domain/)
  })
})

describe('validateRedirect', () => {
  it('accepts a path target for path redirects only', () => {
    expect(validateRedirect(draft(), 'path')).toEqual({})
    expect(validateRedirect(draft({ path: '', target: '/new' }), 'domain').target).toMatch(/full address/)
  })

  it('checks the target', () => {
    expect(validateRedirect(draft({ target: '' }), 'path').target).toMatch(/where the redirect should lead/)
    expect(validateRedirect(draft({ target: '//evil.example' }), 'path').target).toMatch(/must not start with \/\//)
    expect(validateRedirect(draft({ target: 'ftp://example.org' }), 'path').target).toMatch(/isn't a valid target/)
    expect(validateRedirect(draft({ target: 'example.org' }), 'domain').target).toMatch(/https:\/\//)
    expect(validateRedirect(draft({ target: 'https://user:pw@example.org' }), 'path').target).toMatch(/user name/)
    expect(validateRedirect(draft({ target: 'https://example.org/a b' }), 'path').target).toMatch(/spaces/)
    expect(validateRedirect(draft({ target: 'https://example.org/page' }), 'path')).toEqual({})
  })

  it('refuses a ? or # in the target together with keeping path or query', () => {
    expect(validateRedirect(draft({ target: '/new?x=1', keepQuery: true }), 'path').target).toMatch(/\? or #/)
    expect(validateRedirect(draft({ path: '/blog/*', target: '/new#a', keepPath: true }), 'path').target).toMatch(/\? or #/)
    expect(validateRedirect(draft({ target: '/new?x=1', keepPath: true }), 'path')).toEqual({})
  })

  it('refuses targets that lead back to where the redirect applies', () => {
    expect(validateRedirect(draft({ target: '/old' }), 'path').target).toMatch(/forever/)
    expect(validateRedirect(draft({ target: 'https://example.com/old' }), 'path').target).toMatch(/forever/)
    expect(validateRedirect(draft({ path: '/blog/*', target: '/blog/new' }), 'path').target).toMatch(/forever/)
    expect(validateRedirect(draft({ path: '/*', target: '/anything' }), 'path').target).toMatch(/forever/)
    expect(validateRedirect(draft({ path: '', domain: 'go.example.com', target: 'https://go.example.com/x' }), 'domain').target).toMatch(
      /forever/,
    )
    expect(validateRedirect(draft({ path: '/blog/*', target: '/blogger' }), 'path')).toEqual({})
    expect(validateRedirect(draft({ target: 'https://example.com:8443/old' }), 'path')).toEqual({})
  })
})

describe('codes', () => {
  it('names every code in plain words', () => {
    expect(codeLabel(301)).toBe('Permanent (301)')
    expect(codeLabel(302)).toBe('Temporary (302)')
    expect(codeLabel(307)).toBe('Temporary, keeps the method (307)')
    expect(codeLabel(308)).toBe('Permanent, keeps the method (308)')
  })

  it('maps the choices to a code and back', () => {
    expect(codeFor(true, false)).toBe(301)
    expect(codeFor(false, false)).toBe(302)
    expect(codeFor(false, true)).toBe(307)
    expect(codeFor(true, true)).toBe(308)
    const back = draftFrom({ ...redirect(1, 'a.com', '/x'), code: 307 })
    expect([back.permanent, back.keepMethod]).toEqual([false, true])
  })
})

describe('inputFrom', () => {
  it('normalizes the domain and leaves the path out of domain redirects', () => {
    expect(inputFrom(draft({ domain: ' Go.Example.com. ', path: '/x', target: ' https://example.org ' }), 'domain')).toEqual({
      source_domain: 'go.example.com',
      source_path: '',
      target: 'https://example.org',
      code: 301,
      keep_path: false,
      keep_query: false,
    })
  })
})

describe('lists', () => {
  it('sorts by domain, then path', () => {
    const list = [redirect(3, 'b.com', '/z'), redirect(2, 'a.com', '/b'), redirect(1, 'a.com', ''), redirect(4, 'a.com', '/b')]
    expect(sortRedirects(list).map((item) => item.id)).toEqual([1, 2, 4, 3])
    expect(list.map((item) => item.id)).toEqual([3, 2, 1, 4])
  })

  it('keeps only domain redirects', () => {
    expect(domainRedirects([redirect(1, 'a.com', ''), redirect(2, 'a.com', '/x')]).map((item) => item.id)).toEqual([1])
  })

  it('says what is kept, only where it matters', () => {
    expect(keepsSummary({ source_path: '', keep_path: true, keep_query: true })).toEqual([
      'Keeps the rest of the path',
      'Keeps the query',
    ])
    expect(keepsSummary({ source_path: '/old', keep_path: true, keep_query: false })).toEqual([])
    expect(keepsSummary({ source_path: '/blog/*', keep_path: true, keep_query: false })).toEqual([
      'Keeps the rest of the path',
    ])
  })

  it('puts a server message next to the field it is about', () => {
    expect(routeRedirectError('The target leads back to a.com/x, which would redirect again forever', 'path')).toHaveProperty(
      'target',
    )
    expect(routeRedirectError('The path "/x" must start with /', 'path')).toHaveProperty('path')
    expect(routeRedirectError('The domain a.com belongs to the site blog', 'domain')).toHaveProperty('domain')
    expect(routeRedirectError('Each person can have at most 500 redirects', 'domain')).toHaveProperty('form')
  })
})

describe('redirect requests', () => {
  it('sends the right calls', async () => {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) =>
      init?.method === 'DELETE' ? new Response(null, { status: 204 }) : json(init?.method === 'GET' ? null : {}),
    )
    vi.stubGlobal('fetch', fetchMock)
    const input = inputFrom(draft(), 'path')
    await createRedirect(input)
    await updateRedirect(5, input)
    await deleteRedirect(5)
    expect(await listSiteRedirects('my site')).toEqual([])
    expect(fetchMock.mock.calls.map(([url, init]) => `${init?.method} ${url}`)).toEqual([
      'POST /api/redirects',
      'PUT /api/redirects/5',
      'DELETE /api/redirects/5',
      'GET /api/sites/my%20site/redirects',
    ])
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual(input)
  })
})
