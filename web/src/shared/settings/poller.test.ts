import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getPanelChecks, type PanelChecks } from './api.ts'
import { startPolling } from './poller.ts'
import { checksSettled } from './rules.ts'
import { checksFixture, json, mockFetch } from './testing.ts'
import { CHECK_INTERVAL } from './usePanelChecks.ts'

function poll() {
  const results: PanelChecks[] = []
  const errors: unknown[] = []
  const stop = startPolling({
    load: getPanelChecks,
    interval: CHECK_INTERVAL,
    isDone: checksSettled,
    onResult: (checks) => results.push(checks),
    onError: (error) => errors.push(error),
  })
  return { stop, results, errors }
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('polling the panel checks', () => {
  it('asks every 5 seconds and stops once DNS and certificate are ready', async () => {
    const fetchMock = mockFetch(
      json(checksFixture('no_record', 'pending')),
      json(checksFixture('points_here', 'pending')),
      json(checksFixture('points_here', 'ready')),
    )
    const { results } = poll()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/settings/checks')

    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL - 1)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(results.at(-1)?.certificate.state).toBe('ready')

    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL * 5)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('stops when the step is left (unmount)', async () => {
    const fetchMock = mockFetch(json(checksFixture('no_record', 'pending')), json(checksFixture('no_record', 'pending')))
    const { stop } = poll()
    await vi.advanceTimersByTimeAsync(0)
    expect(vi.getTimerCount()).toBe(1)
    stop()
    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL * 3)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('ignores an answer that arrives after stopping', async () => {
    let answer: (response: Response) => void = () => undefined
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_url: string, init?: RequestInit) =>
          new Promise<Response>((resolve, reject) => {
            answer = resolve
            init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))
          }),
      ),
    )
    const { stop, results, errors } = poll()
    stop()
    answer(json(checksFixture('no_record', 'pending')))
    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL * 2)
    expect(results).toEqual([])
    expect(errors).toEqual([])
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps trying after a failed check', async () => {
    const fetchMock = mockFetch(new TypeError('Failed to fetch'), json(checksFixture('points_here', 'ready')))
    const { errors, results } = poll()
    await vi.advanceTimersByTimeAsync(0)
    expect(errors).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(results).toHaveLength(1)
    expect(vi.getTimerCount()).toBe(0)
  })
})
