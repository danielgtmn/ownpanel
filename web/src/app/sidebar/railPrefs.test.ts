import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DEFAULT_PREFS,
  lookOf,
  parsePrefs,
  PREFS_KEY,
  readPrefs,
  updatePrefs,
  withExpanded,
  withInitialsForAll,
  withTileLook,
} from './railPrefs.ts'

class MemoryStorage {
  items = new Map<string, string>()
  getItem(key: string) {
    return this.items.get(key) ?? null
  }
  setItem(key: string, value: string) {
    this.items.set(key, value)
  }
  removeItem(key: string) {
    this.items.delete(key)
  }
}

describe('icon or initials', () => {
  it('shows icons unless initials are chosen for all domains', () => {
    expect(lookOf(DEFAULT_PREFS, 'example.com')).toBe('icon')
    expect(lookOf(withInitialsForAll(DEFAULT_PREFS, true), 'example.com')).toBe('initials')
  })

  it('lets one domain differ from the choice for all', () => {
    const one = withTileLook(DEFAULT_PREFS, 'example.com', 'initials')
    expect(lookOf(one, 'example.com')).toBe('initials')
    expect(lookOf(one, 'shop.de')).toBe('icon')

    const allInitials = withTileLook(withInitialsForAll(DEFAULT_PREFS, true), 'shop.de', 'icon')
    expect(lookOf(allInitials, 'shop.de')).toBe('icon')
    expect(lookOf(allInitials, 'example.com')).toBe('initials')
  })

  it('only remembers domains that differ from the choice for all', () => {
    const back = withTileLook(withTileLook(DEFAULT_PREFS, 'example.com', 'initials'), 'example.com', 'icon')
    expect(back.tiles).toEqual({})
  })

  it('keeps a domain set apart when the choice for all changes, unless it then matches', () => {
    const prefs = withTileLook(withTileLook(DEFAULT_PREFS, 'a.com', 'initials'), 'b.com', 'initials')
    const allInitials = withInitialsForAll(prefs, true)
    expect(allInitials.tiles).toEqual({})
    const withIcon = withTileLook(allInitials, 'a.com', 'icon')
    const allIcons = withInitialsForAll(withIcon, false)
    expect(allIcons.tiles).toEqual({})
    expect(lookOf(withInitialsForAll(withIcon, true), 'a.com')).toBe('icon')
  })

  it('reads stored choices defensively', () => {
    expect(parsePrefs(null)).toEqual(DEFAULT_PREFS)
    expect(parsePrefs('not json')).toEqual(DEFAULT_PREFS)
    expect(parsePrefs('[1]')).toEqual(DEFAULT_PREFS)
    expect(parsePrefs('{"expanded":"yes","initialsForAll":true,"tiles":{"a.com":"initials","b.com":"emoji"}}')).toEqual({
      expanded: false,
      initialsForAll: true,
      tiles: { 'a.com': 'initials' },
    })
  })
})

describe('stored in the browser', () => {
  let storage: MemoryStorage
  beforeEach(() => {
    storage = new MemoryStorage()
    vi.stubGlobal('localStorage', storage)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('persists the icon choices', () => {
    updatePrefs((prefs) => withTileLook(prefs, 'example.com', 'initials'))
    updatePrefs((prefs) => withInitialsForAll(prefs, true))
    expect(JSON.parse(storage.getItem(PREFS_KEY)!)).toEqual({ expanded: false, initialsForAll: true, tiles: {} })
    updatePrefs((prefs) => withTileLook(prefs, 'shop.de', 'icon'))
    expect(JSON.parse(storage.getItem(PREFS_KEY)!).tiles).toEqual({ 'shop.de': 'icon' })
  })

  it('persists the expanded rail and reads it back after a reload', () => {
    expect(readPrefs().expanded).toBe(false)
    updatePrefs((prefs) => withExpanded(prefs, !prefs.expanded))
    expect(JSON.parse(storage.getItem(PREFS_KEY)!).expanded).toBe(true)

    const reloaded = new MemoryStorage()
    reloaded.setItem(PREFS_KEY, storage.getItem(PREFS_KEY)!)
    vi.stubGlobal('localStorage', reloaded)
    expect(readPrefs().expanded).toBe(true)
    updatePrefs((prefs) => withExpanded(prefs, !prefs.expanded))
    expect(readPrefs().expanded).toBe(false)
  })

  it('keeps the choice for the page when storage refuses it', () => {
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: () => {
        throw new Error('quota')
      },
    })
    updatePrefs((prefs) => withExpanded(prefs, true))
    expect(readPrefs().expanded).toBe(true)
  })
})
