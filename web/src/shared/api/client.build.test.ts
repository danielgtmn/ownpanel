import { afterEach, expect, it, vi } from 'vitest'
import { request, setUnauthorizedHandler } from './client.ts'

afterEach(() => {
  vi.unstubAllGlobals()
  setUnauthorizedHandler(null)
})

const answer = (status: number, build: string) =>
  new Response(JSON.stringify(status === 200 ? { ok: true } : { error: 'Please sign in.' }), {
    status,
    headers: { 'Content-Type': 'application/json', 'X-Ownpanel-Build': build },
  })

it('reloads instead of signing out when the panel was updated meanwhile', async () => {
  const reload = vi.fn()
  const items = new Map<string, string>()
  vi.stubGlobal('window', {
    location: { reload },
    sessionStorage: { getItem: (k: string) => items.get(k) ?? null, setItem: (k: string, v: string) => items.set(k, v) },
  })
  const signedOut = vi.fn()
  setUnauthorizedHandler(signedOut)

  vi.stubGlobal('fetch', vi.fn(async () => answer(200, 'old')))
  await expect(request('/sites')).resolves.toEqual({ ok: true })

  // The update changed the login cookie's name, so the next answer is a 401 from the new build.
  vi.stubGlobal('fetch', vi.fn(async () => answer(401, 'new')))
  const pending = request('/sites')
  const settled = await Promise.race([pending.then(() => 'settled', () => 'settled'), new Promise((r) => setTimeout(() => r('pending'), 20))])
  expect(settled).toBe('pending')
  expect(reload).toHaveBeenCalledTimes(1)
  expect(signedOut).not.toHaveBeenCalled()
})
