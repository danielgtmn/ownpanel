import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, errorMessage, isSavedButNotApplied, onDataChange, request, setUnauthorizedHandler } from './client.ts'

function mockFetch(response: Response | Error) {
  const fn = vi.fn(async (_url: string, _init?: RequestInit) => {
    if (response instanceof Error) throw response
    return response
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

const initOf = (fn: ReturnType<typeof mockFetch>) => (fn.mock.calls[0] as [string, RequestInit])[1]

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

beforeEach(() => {
  vi.unstubAllGlobals()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('request', () => {
  it('sends JSON bodies and parses the response', async () => {
    const fetchMock = mockFetch(json({ name: 'a' }))
    const result = await request<{ name: string }>('/sites/a', { method: 'PUT', body: { domains: ['a.com'] } })
    expect(result).toEqual({ name: 'a' })
    const init = initOf(fetchMock)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/sites/a')
    expect(init.method).toBe('PUT')
    expect(init.body).toBe('{"domains":["a.com"]}')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
  })

  it('sends the login cookie with every request and no bearer header', async () => {
    const fetchMock = mockFetch(json([]))
    await request('/sites')
    const init = initOf(fetchMock)
    expect(init.credentials).toBe('same-origin')
    expect(init.headers as Record<string, string>).not.toHaveProperty('Authorization')
  })

  it('marks every request that changes something and leaves reads alone', async () => {
    const read = mockFetch(json({}))
    await request('/sites')
    expect(initOf(read).headers as Record<string, string>).not.toHaveProperty('X-Ownpanel-Request')
    for (const method of ['POST', 'PUT', 'DELETE']) {
      const write = mockFetch(json({}))
      await request('/tokens', { method })
      expect((initOf(write).headers as Record<string, string>)['X-Ownpanel-Request']).toBe('1')
    }
  })

  it('marks file uploads too', async () => {
    const fetchMock = mockFetch(json({}, 201))
    await request('/sites/a/deployments', { method: 'POST', rawBody: new Blob(['x']) })
    expect((initOf(fetchMock).headers as Record<string, string>)['X-Ownpanel-Request']).toBe('1')
  })

  it('sends raw files as a binary stream', async () => {
    const fetchMock = mockFetch(json({ name: 'a' }, 201))
    const file = new Blob(['x'])
    await request('/sites/a/deployments', { method: 'POST', rawBody: file })
    const init = initOf(fetchMock)
    expect(init.body).toBe(file)
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/octet-stream')
  })

  it('returns undefined for 204', async () => {
    mockFetch(new Response(null, { status: 204 }))
    await expect(request('/sites/a', { method: 'DELETE' })).resolves.toBeUndefined()
  })

  it('throws an ApiError carrying the server message and status', async () => {
    mockFetch(json({ error: 'domain example.com is used by another site' }, 400))
    const error = await request('/sites/a').catch((caught: unknown) => caught)
    expect(error).toBeInstanceOf(ApiError)
    expect((error as ApiError).status).toBe(400)
    expect((error as ApiError).message).toBe('domain example.com is used by another site')
  })

  it('falls back to a readable message when the body is not JSON', async () => {
    mockFetch(new Response('<html>bad gateway</html>', { status: 502 }))
    const error = (await request('/sites').catch((caught: unknown) => caught)) as ApiError
    expect(error.status).toBe(502)
    expect(error.message).not.toMatch(/html/)
    expect(error.message.length).toBeGreaterThan(0)
  })

  it('reports an unreachable server as status 0', async () => {
    mockFetch(new TypeError('Failed to fetch'))
    const error = (await request('/sites').catch((caught: unknown) => caught)) as ApiError
    expect(error.status).toBe(0)
    expect(error.message).toMatch(/can't reach/i)
  })

  it('reports a 401 to the sign-out handler unless told otherwise', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    mockFetch(json({ error: 'unauthorized' }, 401))
    await request('/sites', { silentUnauthorized: true }).catch(() => undefined)
    expect(handler).not.toHaveBeenCalled()
    const error = (await request('/sites').catch((caught: unknown) => caught)) as ApiError
    expect(handler).toHaveBeenCalledTimes(1)
    expect(error.status).toBe(401)
    setUnauthorizedHandler(null)
  })

  it('does not treat other failures as a sign-out', async () => {
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    for (const status of [400, 403, 409, 429, 500]) {
      mockFetch(json({ error: 'nope' }, status))
      await request('/sites').catch(() => undefined)
    }
    expect(handler).not.toHaveBeenCalled()
    setUnauthorizedHandler(null)
  })

  it('shows the server message for too many attempts', async () => {
    mockFetch(json({ error: 'Too many sign-in attempts. Try again in 5 minutes.' }, 429))
    const error = (await request('/login', { method: 'POST' }).catch((caught: unknown) => caught)) as ApiError
    expect(error.status).toBe(429)
    expect(error.message).toBe('Too many sign-in attempts. Try again in 5 minutes.')
  })

  it('has readable fallbacks for 403 and 429', async () => {
    mockFetch(new Response('', { status: 403 }))
    expect(((await request('/users').catch((c: unknown) => c)) as ApiError).message).toMatch(/not allowed/i)
    mockFetch(new Response('', { status: 429 }))
    expect(((await request('/login').catch((c: unknown) => c)) as ApiError).message).toMatch(/too many attempts/i)
  })
})

describe('onDataChange', () => {
  it('tells listeners about changes that went through, not about reads or refusals', async () => {
    const listener = vi.fn()
    const stop = onDataChange(listener)
    mockFetch(json([]))
    await request('/sites')
    mockFetch(json({ error: 'no' }, 400))
    await request('/sites/a', { method: 'PUT' }).catch(() => undefined)
    expect(listener).not.toHaveBeenCalled()
    mockFetch(json({}))
    await request('/sites/a', { method: 'PUT' })
    mockFetch(json({ error: 'saved, web server not updated' }, 502))
    await request('/redirects', { method: 'POST' }).catch(() => undefined)
    expect(listener).toHaveBeenCalledTimes(2)
    stop()
    mockFetch(json({}))
    await request('/sites/a', { method: 'DELETE' })
    expect(listener).toHaveBeenCalledTimes(2)
  })
})

describe('error helpers', () => {
  it('recognises a saved-but-not-applied response', () => {
    expect(isSavedButNotApplied(new ApiError(502, 'x'))).toBe(true)
    expect(isSavedButNotApplied(new ApiError(500, 'x'))).toBe(false)
    expect(isSavedButNotApplied(new Error('x'))).toBe(false)
  })

  it('shows API messages and hides unknown errors', () => {
    expect(errorMessage(new ApiError(409, 'Nothing to go back to.'))).toBe('Nothing to go back to.')
    expect(errorMessage(new Error('boom'))).toBe('Something went wrong. Try again.')
  })
})
