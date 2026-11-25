import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { InstalledVersion } from './UpdatesSection.tsx'
import type { UpdateInfo } from './updatesApi.ts'

const info: UpdateInfo = {
  installed: { version: 'v1.2.0', channel: 'stable' },
  available: { stable: null, nightly: null },
  check_failed: false,
  check_error: '',
  checked_at: '2026-11-25T12:00:00Z',
  update_available: false,
  auto: { mode: 'off', window: '03:00-05:00' },
  status: { state: 'idle', step: '', message: '', started_at: '', finished_at: '', from: '', to: '' },
  supported: true,
}
const now = new Date('2026-11-25T12:12:00Z')
const check = () => Promise.resolve()

describe('installed version', () => {
  it('offers a check and tells when the panel last looked', () => {
    const html = renderToStaticMarkup(<InstalledVersion info={info} busy={false} now={now} onCheck={check} />)
    expect(html).toContain('v1.2.0')
    expect(html).toContain('Stable releases')
    expect(html).toContain('Last checked 12 minutes ago')
    expect(html).toMatch(/<button[^>]*>Check for updates<\/button>/)
    expect(html).not.toMatch(/<button[^>]*disabled/)
  })

  it('cannot check while an update runs', () => {
    const html = renderToStaticMarkup(<InstalledVersion info={info} busy now={now} onCheck={check} />)
    expect(html).toMatch(/<button[^>]*disabled[^>]*>Check for updates/)
  })

  it('leaves out the time before the panel ever looked', () => {
    const html = renderToStaticMarkup(<InstalledVersion info={{ ...info, checked_at: '' }} busy={false} now={now} onCheck={check} />)
    expect(html).not.toContain('Last checked')
  })
})
