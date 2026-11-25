import { describe, expect, it } from 'vitest'
import {
  checkResult,
  lastCheckedText,
  needsReload,
  progressText,
  resultNotice,
  splitWindow,
  updateQuestion,
  windowProblem,
} from './updates.ts'
import type { UpdateInfo, UpdateStatus } from './updatesApi.ts'

const idle: UpdateStatus = { state: 'idle', step: '', message: '', started_at: '', finished_at: '', from: '', to: '' }

function info(over: Partial<UpdateInfo> = {}): UpdateInfo {
  return {
    installed: { version: 'v1.1.0', channel: 'stable' },
    available: {
      stable: { version: 'v1.2.0', published_at: '2026-11-20T10:00:00Z', notes_url: 'https://x', older: false },
      nightly: { version: 'nightly-20261124-878136d', published_at: '2026-11-24T10:00:00Z', notes_url: 'https://y', older: false },
    },
    check_failed: false,
    check_error: '',
    checked_at: '2026-11-25T12:00:00Z',
    update_available: true,
    auto: { mode: 'off', window: '03:00-05:00' },
    status: idle,
    supported: true,
    ...over,
  }
}

describe('updateQuestion', () => {
  it('asks plainly for an update within the channel', () => {
    const q = updateQuestion(info(), 'stable')
    expect(q.title).toBe('Update to v1.2.0?')
    expect(q.confirmLabel).toBe('Update to v1.2.0')
  })

  it('warns when switching channels installs an older version', () => {
    const i = info({
      installed: { version: 'nightly-20261124-878136d', channel: 'nightly' },
      available: { ...info().available, stable: { ...info().available.stable!, older: true } },
    })
    const q = updateQuestion(i, 'stable')
    expect(q.title).toMatch(/go back to v1\.2\.0/)
    expect(q.description).toMatch(/older than the installed nightly-20261124-878136d/)
  })

  it('explains a switch to a newer version without a warning', () => {
    const q = updateQuestion(info(), 'nightly')
    expect(q.title).toBe('Switch to nightly builds?')
    expect(q.description).not.toMatch(/older/)
  })
})

describe('progress and results', () => {
  it('shows restarting while the panel does not answer', () => {
    expect(progressText({ ...idle, state: 'running', step: 'installing' }, true)).toMatch(/Restarting/)
    expect(progressText({ ...idle, state: 'running', step: 'downloading' }, false)).toMatch(/verifying/)
    expect(progressText({ ...idle, state: 'requested' }, false)).toMatch(/Waiting/)
  })

  it('describes each result', () => {
    expect(resultNotice(idle)).toBeNull()
    expect(resultNotice({ ...idle, state: 'running' })).toBeNull()
    expect(resultNotice({ ...idle, state: 'succeeded', from: 'v1.1.0', to: 'v1.2.0' })?.title).toBe('Updated from v1.1.0 to v1.2.0')
    const back = resultNotice({ ...idle, state: 'rolled_back', from: 'v1.1.0', to: 'v1.2.0' })
    expect(back?.tone).toBe('warning')
    expect(back?.detail).toMatch(/v1\.1\.0 was put back/)
    expect(resultNotice({ ...idle, state: 'failed', message: 'No space left.' })?.detail).toBe('No space left.')
  })

  it('reloads only once a different version is running', () => {
    expect(needsReload('v1.1.0', info())).toBe(false)
    expect(needsReload('v1.1.0', info({ installed: { version: 'v1.2.0', channel: 'stable' } }))).toBe(true)
    expect(
      needsReload('v1.1.0', info({ installed: { version: 'v1.2.0', channel: 'stable' }, status: { ...idle, state: 'running' } })),
    ).toBe(false)
    expect(needsReload(null, info())).toBe(false)
  })
})

describe('time window', () => {
  it('splits and checks the window', () => {
    expect(splitWindow('22:30-01:00')).toEqual(['22:30', '01:00'])
    expect(windowProblem('03:00', '03:00')).toMatch(/differ/)
    expect(windowProblem(null, '03:00')).toMatch(/both/)
    expect(windowProblem('22:00', '02:00')).toBeNull()
  })
})

describe('checking for updates', () => {
  const at = '2026-11-25T12:00:00Z'
  const after = (seconds: number) => new Date(Date.parse(at) + seconds * 1000)

  it('tells when the panel last looked', () => {
    expect(lastCheckedText(at, after(5))).toBe('Last checked just now')
    expect(lastCheckedText(at, after(-3))).toBe('Last checked just now')
    expect(lastCheckedText(at, after(60))).toBe('Last checked 1 minute ago')
    expect(lastCheckedText(at, after(12 * 60 + 30))).toBe('Last checked 12 minutes ago')
    expect(lastCheckedText(at, after(3 * 3600))).toBe('Last checked 3 hours ago')
    expect(lastCheckedText(at, after(2 * 86400))).toBe('Last checked 2 days ago')
    expect(lastCheckedText('', after(0))).toBeNull()
    expect(lastCheckedText('garbage', after(0))).toBeNull()
  })

  it('finds a newer version of the installed channel', () => {
    expect(checkResult(info())).toEqual({ kind: 'update', version: 'v1.2.0' })
  })

  it('is up to date when the newest version is installed, or only an older one exists', () => {
    expect(checkResult(info({ installed: { version: 'v1.2.0', channel: 'stable' }, update_available: false }))).toEqual({ kind: 'current' })
    const older = info({ available: { ...info().available, stable: { ...info().available.stable!, older: true } } })
    expect(checkResult(older)).toEqual({ kind: 'current' })
    expect(checkResult(info({ available: { stable: null, nightly: null } }))).toEqual({ kind: 'current' })
  })

  it('reports a failed check', () => {
    expect(checkResult(info({ check_failed: true, check_error: 'GitHub answered 403' }))).toEqual({ kind: 'failed' })
  })
})
