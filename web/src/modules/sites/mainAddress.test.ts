import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Redirect } from '../../shared/api/index.ts'
import {
  addressPairs,
  currentSetup,
  describeStep,
  mainRedirect,
  pairState,
  pathRedirectsLost,
  planMainAddress,
  runPlan,
  type Step,
} from './mainAddress.ts'

const pair = { bare: 'example.com', www: 'www.example.com' }

function redirect(id: string, from: string, target: string, overrides: Partial<Redirect> = {}): Redirect {
  return {
    id,
    owner: 'anna',
    source_domain: from,
    source_path: '',
    target,
    code: 301,
    keep_path: true,
    keep_query: true,
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

const state = (domains: string[], redirects: Redirect[] = []) => pairState(pair, domains, redirects)

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('addressPairs', () => {
  it('pairs a domain with its www form once', () => {
    expect(addressPairs(['example.com', 'www.example.com', 'blog.example.org'])).toEqual([
      { bare: 'example.com', www: 'www.example.com' },
      { bare: 'blog.example.org', www: 'www.blog.example.org' },
    ])
  })

  it('works from the www form alone and skips names without a dot', () => {
    expect(addressPairs(['www.example.com'])).toEqual([pair])
    expect(addressPairs(['www.localhost'])).toEqual([])
  })
})

describe('currentSetup', () => {
  it('recognises each starting point', () => {
    expect(currentSetup(state(['example.com', 'www.example.com']))).toEqual({ kind: 'both' })
    expect(currentSetup(state(['example.com']))).toMatchObject({ kind: 'choice', choice: 'none', main: 'example.com' })
    expect(currentSetup(state(['example.com'], [redirect('1', 'www.example.com', 'https://example.com')]))).toMatchObject({
      kind: 'choice',
      choice: 'bare',
      other: 'www.example.com',
    })
    expect(currentSetup(state(['www.example.com'], [redirect('1', 'example.com', 'https://www.example.com/')]))).toMatchObject(
      { kind: 'choice', choice: 'www', other: 'example.com' },
    )
    expect(currentSetup(state(['example.com'], [redirect('1', 'www.example.com', 'https://other.org')]))).toEqual({
      kind: 'elsewhere',
      main: 'example.com',
      other: 'www.example.com',
      target: 'https://other.org',
    })
  })
})

describe('planMainAddress', () => {
  it('takes www off the site before redirecting it, when both are site domains', () => {
    expect(planMainAddress(state(['example.com', 'www.example.com']), 'bare')).toEqual([
      { kind: 'set-domains', domains: ['example.com'], added: [], removed: ['www.example.com'] },
      { kind: 'create-redirect', input: mainRedirect('www.example.com', 'example.com') },
    ])
  })

  it('redirects the bare domain to www', () => {
    expect(planMainAddress(state(['example.com', 'www.example.com', 'a.org']), 'www')).toEqual([
      { kind: 'set-domains', domains: ['www.example.com', 'a.org'], added: [], removed: ['example.com'] },
      { kind: 'create-redirect', input: mainRedirect('example.com', 'www.example.com') },
    ])
  })

  it('adds the main address when only the other one is a site domain', () => {
    expect(planMainAddress(state(['www.example.com']), 'bare')).toEqual([
      { kind: 'set-domains', domains: ['example.com'], added: ['example.com'], removed: ['www.example.com'] },
      { kind: 'create-redirect', input: mainRedirect('www.example.com', 'example.com') },
    ])
  })

  it('removes the old redirect first when switching sides', () => {
    const now = state(['www.example.com'], [redirect('7', 'example.com', 'https://www.example.com')])
    expect(planMainAddress(now, 'bare')).toEqual([
      { kind: 'delete-redirect', id: '7', domain: 'example.com' },
      { kind: 'set-domains', domains: ['example.com'], added: ['example.com'], removed: ['www.example.com'] },
      { kind: 'create-redirect', input: mainRedirect('www.example.com', 'example.com') },
    ])
  })

  it('only adds the redirect when the site already uses just the main address', () => {
    expect(planMainAddress(state(['example.com']), 'bare')).toEqual([
      { kind: 'create-redirect', input: mainRedirect('www.example.com', 'example.com') },
    ])
  })

  it('changes a redirect that leads elsewhere instead of adding a second one', () => {
    const now = state(['example.com'], [redirect('3', 'www.example.com', 'https://other.org', { code: 302 })])
    expect(planMainAddress(now, 'bare')).toEqual([
      { kind: 'update-redirect', id: '3', input: mainRedirect('www.example.com', 'example.com') },
    ])
  })

  it('changes nothing when the choice is already in place', () => {
    const now = state(['example.com'], [redirect('1', 'www.example.com', 'https://example.com/')])
    expect(planMainAddress(now, 'bare')).toEqual([])
    expect(planMainAddress(state(['example.com']), 'none')).toEqual([])
    expect(planMainAddress(state(['www.example.com'], [redirect('2', 'example.com', 'https://www.example.com')]), 'www')).toEqual(
      [],
    )
  })

  it('fixes a redirect that keeps too little', () => {
    const now = state(['example.com'], [redirect('1', 'www.example.com', 'https://example.com', { keep_query: false })])
    expect(planMainAddress(now, 'bare')).toEqual([
      { kind: 'update-redirect', id: '1', input: mainRedirect('www.example.com', 'example.com') },
    ])
  })

  it('stops using the other address', () => {
    const redirected = state(['example.com'], [redirect('4', 'www.example.com', 'https://example.com')])
    expect(planMainAddress(redirected, 'none')).toEqual([{ kind: 'delete-redirect', id: '4', domain: 'www.example.com' }])
    const wwwMain = state(['www.example.com'], [redirect('5', 'example.com', 'https://www.example.com')])
    expect(planMainAddress(wwwMain, 'none')).toEqual([{ kind: 'delete-redirect', id: '5', domain: 'example.com' }])
    expect(planMainAddress(state(['example.com', 'www.example.com']), 'none')).toEqual([
      { kind: 'set-domains', domains: ['example.com'], added: [], removed: ['www.example.com'] },
    ])
  })

  it('uses a permanent redirect that keeps path and query', () => {
    expect(mainRedirect('www.example.com', 'example.com')).toEqual({
      source_domain: 'www.example.com',
      source_path: '',
      target: 'https://example.com',
      code: 301,
      keep_path: true,
      keep_query: true,
    })
  })
})

describe('describeStep and lost path redirects', () => {
  const steps = planMainAddress(state(['www.example.com'], [redirect('7', 'example.com', 'https://www.example.com')]), 'bare')

  it('describes every step in plain words', () => {
    expect(steps.map((step) => describeStep(step, 'blog'))).toEqual([
      'Stop redirecting example.com',
      'Add example.com to blog and remove www.example.com from blog',
      'Redirect www.example.com to example.com',
    ])
    const update: Step = { kind: 'update-redirect', id: '1', input: mainRedirect('www.example.com', 'example.com') }
    expect(describeStep(update, 'blog')).toBe('Change the redirect of www.example.com to lead to example.com')
  })

  it('counts path redirects on a domain that leaves the site', () => {
    expect(
      pathRedirectsLost(steps, [{ source_domain: 'www.example.com' }, { source_domain: 'example.com' }]),
    ).toBe(1)
  })
})

describe('runPlan', () => {
  const steps = planMainAddress(state(['www.example.com'], [redirect('7', 'example.com', 'https://www.example.com')]), 'bare')

  it('runs the steps in order', async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') return new Response(null, { status: 204 })
      if (url.startsWith('/api/sites/')) return json({ name: 'blog', domains: ['example.com'] })
      return json(redirect('8', 'www.example.com', 'https://example.com'), 201)
    })
    vi.stubGlobal('fetch', fetchMock)
    const result = await runPlan('blog', steps)
    expect(result.failed).toBeNull()
    expect(result.done).toHaveLength(3)
    expect(fetchMock.mock.calls.map(([url, init]) => `${init?.method} ${url}`)).toEqual([
      'DELETE /api/redirects/7',
      'PUT /api/sites/blog',
      'POST /api/redirects',
    ])
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toEqual({ domains: ['example.com'] })
  })

  it('stops at the first refusal and tells what was done and what not', async () => {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') return new Response(null, { status: 204 })
      return json({ error: 'The domain example.com is already used by another site' }, 400)
    })
    vi.stubGlobal('fetch', fetchMock)
    const result = await runPlan('blog', steps)
    expect(result.done).toEqual([steps[0]])
    expect(result.failed).toEqual({ step: steps[1], message: 'The domain example.com is already used by another site' })
    expect(result.remaining).toEqual([steps[2]])
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('goes on when a change is saved but not yet applied', async () => {
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === 'DELETE') return json({ error: 'Saved, but the web server was not updated' }, 502)
      return json({}, 200)
    })
    vi.stubGlobal('fetch', fetchMock)
    const result = await runPlan('blog', steps)
    expect(result.failed).toBeNull()
    expect(result.done).toHaveLength(3)
    expect(result.warnings).toEqual(['Saved, but the web server was not updated'])
  })
})
