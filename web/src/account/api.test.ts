import { afterEach, describe, expect, it, vi } from 'vitest'
import { AccountError, changePassword, loadMe, onSessionEnded, signIn } from './api.ts'

function respond(status: number, body: unknown) {
  return vi.fn(async (..._args: unknown[]) => new Response(body === undefined ? null : JSON.stringify(body), { status }))
}

afterEach(() => {
  vi.unstubAllGlobals()
  onSessionEnded(null)
})

describe('account API', () => {
  it('talks only to the mail server’s own API and marks changes', async () => {
    const fetch = respond(200, { address: 'anna@example.com' })
    vi.stubGlobal('fetch', fetch)
    await changePassword('old-password', 'new-password-1', true)
    const [url, init] = fetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/account/api/password')
    expect(init.method).toBe('PUT')
    expect((init.headers as Record<string, string>)['X-Ownpanel-Request']).toBe('1')
    expect(JSON.parse(init.body as string)).toEqual({
      current_password: 'old-password',
      new_password: 'new-password-1',
      revoke_app_passwords: true,
    })
    await loadMe()
    const [, get] = fetch.mock.calls[1] as [string, RequestInit]
    expect((get.headers as Record<string, string>)['X-Ownpanel-Request']).toBeUndefined()
  })

  it('reports an ended session, but not a wrong password at sign-in', async () => {
    const ended = vi.fn()
    onSessionEnded(ended)
    vi.stubGlobal('fetch', respond(401, { error: 'Wrong address or password.' }))
    await expect(signIn('anna@example.com', 'wrong')).rejects.toThrow('Wrong address or password.')
    expect(ended).not.toHaveBeenCalled()
    vi.stubGlobal('fetch', respond(401, { error: 'Your session has ended. Sign in again.' }))
    await expect(loadMe()).rejects.toBeInstanceOf(AccountError)
    expect(ended).toHaveBeenCalledTimes(1)
  })

  it('falls back to a plain message', async () => {
    vi.stubGlobal('fetch', respond(500, undefined))
    await expect(loadMe()).rejects.toThrow('Something went wrong on the server')
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('offline'))))
    await expect(loadMe()).rejects.toThrow("Can't reach the mail server")
  })
})
