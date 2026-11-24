import { afterEach, describe, expect, it, vi } from 'vitest'
import { request } from '../api/index.ts'
import { signInErrorMessage } from './messages.ts'
import {
  changePassword,
  completeSetup,
  getSessionState,
  loadSession,
  signIn,
  signOutNow,
  SIGNED_OUT_NOTICE,
  toSessionState,
} from './store.ts'
import type { User } from './types.ts'

const anna: User = { id: 1, name: 'anna', role: 'admin', created_at: '2026-01-01T00:00:00Z' }

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function mockFetch(...responses: Array<Response | Error>) {
  const queue = [...responses]
  const fn = vi.fn(async (_url: string, _init?: RequestInit) => {
    const next = queue.shift() ?? json({})
    if (next instanceof Error) throw next
    return next
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

const call = (fn: ReturnType<typeof mockFetch>, index: number) => fn.mock.calls[index] as [string, RequestInit]

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('toSessionState', () => {
  it('maps the three states the server can report', () => {
    expect(toSessionState({ setup_required: true, user: null, via: null })).toEqual({ status: 'setup' })
    expect(toSessionState({ setup_required: false, user: null, via: null })).toEqual({
      status: 'signedOut',
      notice: null,
    })
    expect(toSessionState({ setup_required: false, user: anna, via: 'session' })).toEqual({
      status: 'signedIn',
      user: anna,
    })
  })
})

describe('session bootstrap', () => {
  it('reports an unreachable server instead of a sign-in screen', async () => {
    mockFetch(new TypeError('Failed to fetch'))
    await loadSession()
    expect(getSessionState()).toMatchObject({ status: 'unreachable', message: expect.stringMatching(/can't reach/i) })
  })

  it('shows setup when the server needs it', async () => {
    mockFetch(json({ setup_required: true, user: null, via: null }))
    await loadSession()
    expect(getSessionState()).toEqual({ status: 'setup' })
  })

  it('shows the sign-in screen when nobody is signed in', async () => {
    mockFetch(json({ setup_required: false, user: null, via: null }))
    await loadSession()
    expect(getSessionState()).toEqual({ status: 'signedOut', notice: null })
  })

  it('signs in when the server knows the person', async () => {
    mockFetch(json({ setup_required: false, user: anna, via: 'session' }))
    await loadSession()
    expect(getSessionState()).toEqual({ status: 'signedIn', user: anna })
  })

  it('keeps a notice for the sign-in screen', async () => {
    mockFetch(json({ setup_required: false, user: null, via: null }))
    await loadSession('Please sign in.')
    expect(getSessionState()).toEqual({ status: 'signedOut', notice: 'Please sign in.' })
  })
})

describe('signing in and out', () => {
  it('posts the credentials with the request marker and becomes signed in', async () => {
    const fetchMock = mockFetch(json(anna))
    await signIn('anna', 'correct horse battery')
    const [url, init] = call(fetchMock, 0)
    expect(url).toBe('/api/login')
    expect(init.method).toBe('POST')
    expect(init.body).toBe('{"name":"anna","password":"correct horse battery"}')
    expect((init.headers as Record<string, string>)['X-Ownpanel-Request']).toBe('1')
    expect(getSessionState()).toEqual({ status: 'signedIn', user: anna })
  })

  it('explains wrong credentials in plain words', async () => {
    mockFetch(json({ error: 'invalid name or password' }, 401))
    const error = await signIn('anna', 'nope').catch((caught: unknown) => caught)
    expect(signInErrorMessage(error)).toMatch(/don't match/i)
  })

  it('shows the server message when there were too many attempts', async () => {
    mockFetch(json({ error: 'Too many attempts. Try again in a few minutes.' }, 429))
    const error = await signIn('anna', 'nope').catch((caught: unknown) => caught)
    expect(signInErrorMessage(error)).toBe('Too many attempts. Try again in a few minutes.')
  })

  it('completes the setup with the code, name and password', async () => {
    const fetchMock = mockFetch(json(anna, 201))
    await completeSetup('abc-123', 'anna', 'correct horse battery')
    expect(call(fetchMock, 0)[0]).toBe('/api/setup')
    expect(call(fetchMock, 0)[1].body).toBe('{"setup_code":"abc-123","name":"anna","password":"correct horse battery"}')
    expect(getSessionState()).toEqual({ status: 'signedIn', user: anna })
  })

  it('signs out', async () => {
    mockFetch(json(anna))
    await signIn('anna', 'correct horse battery')
    const fetchMock = mockFetch(new Response(null, { status: 204 }))
    await signOutNow()
    expect(call(fetchMock, 0)[0]).toBe('/api/logout')
    expect(getSessionState()).toEqual({ status: 'signedOut', notice: null })
  })
})

describe('an ended login', () => {
  it('returns to the sign-in screen with a notice when any call answers 401', async () => {
    mockFetch(json(anna))
    await signIn('anna', 'correct horse battery')
    mockFetch(json({ error: 'sign in first' }, 401))
    await request('/sites').catch(() => undefined)
    expect(getSessionState()).toEqual({ status: 'signedOut', notice: SIGNED_OUT_NOTICE })
  })

  it('ignores a 401 when nobody is signed in', async () => {
    mockFetch(json({ setup_required: true, user: null, via: null }))
    await loadSession()
    mockFetch(json({ error: 'sign in first' }, 401))
    await request('/sites').catch(() => undefined)
    expect(getSessionState()).toEqual({ status: 'setup' })
  })
})

describe('changePassword', () => {
  it('sends the current and new password', async () => {
    const fetchMock = mockFetch(new Response(null, { status: 204 }))
    await changePassword('old password 1', 'new password 2')
    const [url, init] = call(fetchMock, 0)
    expect(url).toBe('/api/account/password')
    expect(init.method).toBe('PUT')
    expect(init.body).toBe('{"current":"old password 1","new":"new password 2"}')
  })

  it('keeps the person signed in when a 401 only means a wrong current password', async () => {
    mockFetch(json(anna))
    await signIn('anna', 'correct horse battery')
    mockFetch(json({ error: 'current password is wrong' }, 401), json({ setup_required: false, user: anna, via: 'session' }))
    await expect(changePassword('wrong', 'new password 2')).rejects.toThrow('current password is wrong')
    expect(getSessionState()).toEqual({ status: 'signedIn', user: anna })
  })

  it('signs out when a 401 really means the login ended', async () => {
    mockFetch(json(anna))
    await signIn('anna', 'correct horse battery')
    mockFetch(json({ error: 'sign in first' }, 401), json({ setup_required: false, user: null, via: null }))
    await expect(changePassword('x', 'new password 2')).rejects.toThrow()
    expect(getSessionState()).toEqual({ status: 'signedOut', notice: SIGNED_OUT_NOTICE })
  })
})
