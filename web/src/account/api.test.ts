import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  AccountError,
  changePassword,
  loadFilters,
  loadMe,
  onSessionEnded,
  setDelegations,
  setFilterRules,
  setForwarding,
  setOutOfOffice,
  signIn,
} from './api.ts'

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

  it('sends the whole list of people the mailbox is shared with and always answers with lists', async () => {
    const fetch = respond(200, { address: 'anna@example.com', shared_with: null })
    vi.stubGlobal('fetch', fetch)
    const me = await setDelegations([{ address: 'ben@example.com', rights: ['read', 'send-on-behalf'] }])
    const [url, init] = fetch.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/account/api/delegations')
    expect(init.method).toBe('PUT')
    expect((init.headers as Record<string, string>)['X-Ownpanel-Request']).toBe('1')
    expect(JSON.parse(init.body as string)).toEqual({
      grants: [{ address: 'ben@example.com', rights: ['read', 'send-on-behalf'] }],
    })
    expect(me.shared_with).toEqual([])
    expect(me.access).toEqual([])
    expect(me.app_passwords).toEqual([])
  })

  it('saves filters with only the fields the server knows, and replace only when asked', async () => {
    const answer = {
      rules: { managed: true, out_of_office: { enabled: false }, forwarding: { enabled: false }, filters: null, active_script: '' },
      folders: null,
      may_forward: true,
    }
    const fetch = respond(200, answer)
    vi.stubGlobal('fetch', fetch)
    const loaded = await loadFilters()
    expect(loaded.folders).toEqual([])
    expect(loaded.rules.filters).toEqual([])
    const outOfOffice = { enabled: true, subject: '', text: 'Away', start: '', end: '', zone: '+0100', days: 7 }
    await setOutOfOffice(outOfOffice, false)
    await setForwarding({ enabled: true, to: 'b@example.com', keep_copy: true }, true)
    await setFilterRules([], false)
    const bodies = fetch.mock.calls.slice(1).map(([url, init]) => [url, JSON.parse((init as RequestInit).body as string)])
    expect(bodies).toEqual([
      ['/account/api/out-of-office', { out_of_office: outOfOffice }],
      ['/account/api/forwarding', { forwarding: { enabled: true, to: 'b@example.com', keep_copy: true }, replace: true }],
      ['/account/api/filters', { filters: [] }],
    ])
  })

  it('names the managed mailbox in every filter request', async () => {
    const answer = {
      rules: { managed: true, out_of_office: { enabled: false }, forwarding: { enabled: false }, filters: null, active_script: '' },
      folders: null,
      may_forward: true,
      mailbox: 'team@example.com',
    }
    const fetch = respond(200, answer)
    vi.stubGlobal('fetch', fetch)
    const loaded = await loadFilters('team@example.com')
    expect(loaded.mailbox).toBe('team@example.com')
    await setForwarding({ enabled: false, to: '', keep_copy: false }, false, 'team@example.com')
    await setFilterRules([], false, 'team@example.com')
    const reply = { enabled: false, subject: '', text: '', start: '', end: '', zone: '', days: 7 }
    await setOutOfOffice(reply, false, 'team@example.com')
    expect(fetch.mock.calls.map(([url]) => url)).toEqual([
      '/account/api/filters?mailbox=team%40example.com',
      '/account/api/forwarding?mailbox=team%40example.com',
      '/account/api/filters?mailbox=team%40example.com',
      '/account/api/out-of-office?mailbox=team%40example.com',
    ])
  })

  it('explains a refusal because of filters from a mail program', async () => {
    vi.stubGlobal('fetch', respond(409, { error: 'script is not managed' }))
    await expect(setFilterRules([], false)).rejects.toThrow('Your filters were written in a mail program.')
  })

  it('falls back to a plain message', async () => {
    vi.stubGlobal('fetch', respond(500, undefined))
    await expect(loadMe()).rejects.toThrow('Something went wrong on the server')
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('offline'))))
    await expect(loadMe()).rejects.toThrow("Can't reach the mail server")
  })
})
