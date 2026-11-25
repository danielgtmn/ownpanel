import { describe, expect, it, vi } from 'vitest'
import { createBuildWatch } from './build.ts'

function memoryStorage() {
  const items = new Map<string, string>()
  return {
    getItem: (key: string) => items.get(key) ?? null,
    setItem: (key: string, value: string) => void items.set(key, value),
  }
}

describe('createBuildWatch', () => {
  it('remembers the first build and reloads once it changes', () => {
    const reload = vi.fn()
    const notice = createBuildWatch({ reload, storage: memoryStorage })
    expect(notice('a')).toBe(false)
    expect(notice('a')).toBe(false)
    expect(reload).not.toHaveBeenCalled()
    expect(notice('b')).toBe(true)
    expect(reload).toHaveBeenCalledTimes(1)
    // Answers still coming in while the page reloads don't reload again.
    expect(notice('b')).toBe(true)
    expect(notice(null)).toBe(true)
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('ignores answers without a build', () => {
    const reload = vi.fn()
    const notice = createBuildWatch({ reload, storage: memoryStorage })
    expect(notice(null)).toBe(false)
    expect(notice('a')).toBe(false)
    expect(notice(null)).toBe(false)
    expect(reload).not.toHaveBeenCalled()
  })

  it('reloads for the same change only once, so it cannot loop', () => {
    const storage = memoryStorage()
    const reload = vi.fn()
    // The page after the reload is a new page with a new watch, but the same storage.
    const first = createBuildWatch({ reload, storage: () => storage })
    first('a')
    expect(first('b')).toBe(true)
    const second = createBuildWatch({ reload, storage: () => storage })
    second('a')
    expect(second('b')).toBe(false)
    expect(reload).toHaveBeenCalledTimes(1)
    // A later, different change reloads again.
    const third = createBuildWatch({ reload, storage: () => storage })
    third('b')
    expect(third('c')).toBe(true)
    expect(reload).toHaveBeenCalledTimes(2)
  })

  it('does not reload when it cannot remember having done so', () => {
    const reload = vi.fn()
    const none = createBuildWatch({ reload, storage: () => null })
    none('a')
    expect(none('b')).toBe(false)
    const refused = createBuildWatch({
      reload,
      storage: () => ({
        getItem: () => null,
        setItem: () => {
          throw new Error('quota')
        },
      }),
    })
    refused('a')
    expect(refused('b')).toBe(false)
    expect(reload).not.toHaveBeenCalled()
  })
})
