import { afterEach, describe, expect, it, vi } from 'vitest'
import { getSettings } from './api.ts'
import { finishWizard, saveSettings } from './save.ts'
import { call, json, mockFetch, settingsFixture } from './testing.ts'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('saving settings', () => {
  it('sends only the changed field with the request marker', async () => {
    const saved = settingsFixture({ panel_domain: 'panel.example.com' })
    const fetchMock = mockFetch(json(saved))
    const result = await saveSettings({ panel_domain: 'panel.example.com' })
    const [url, init] = call(fetchMock, 0)
    expect(url).toBe('/api/settings')
    expect(init.method).toBe('PUT')
    expect(init.body).toBe('{"panel_domain":"panel.example.com"}')
    expect((init.headers as Record<string, string>)['X-Ownpanel-Request']).toBe('1')
    expect(result).toEqual({ settings: saved, applied: true })
  })

  it('reads the saved state back when the change is stored but not in effect yet', async () => {
    const saved = settingsFixture({ server_ips: ['203.0.113.5'] })
    const fetchMock = mockFetch(json({ error: 'saved, but the web server could not be updated yet: down' }, 502), json(saved))
    const result = await saveSettings({ server_ips: ['203.0.113.5'] })
    expect(call(fetchMock, 1)[0]).toBe('/api/settings')
    expect(call(fetchMock, 1)[1].method).toBe('GET')
    expect(result).toEqual({ settings: saved, applied: false })
  })

  it('passes other errors on with the server message', async () => {
    mockFetch(json({ error: 'This value is set in the server configuration (-panel-domain) and cannot be changed here.' }, 409))
    await expect(saveSettings({ panel_domain: 'x.example.com' })).rejects.toMatchObject({ status: 409 })
  })

  it('finishes the setup', async () => {
    const done = settingsFixture({ setup_complete: true })
    const fetchMock = mockFetch(json(done))
    expect(await finishWizard()).toEqual({ settings: done, applied: true })
    expect(call(fetchMock, 0)[0]).toBe('/api/settings/complete')
    expect(call(fetchMock, 0)[1].method).toBe('POST')
  })

  it('fills in missing lists', async () => {
    mockFetch(json({ ...settingsFixture(), server_ips: null, detected_ips: null }))
    const settings = await getSettings()
    expect(settings.server_ips).toEqual([])
    expect(settings.detected_ips).toEqual([])
  })
})
