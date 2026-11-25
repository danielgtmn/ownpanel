import { useSyncExternalStore } from 'react'

export type TileLook = 'icon' | 'initials'

export type RailPrefs = {
  expanded: boolean
  initialsForAll: boolean
  // Only tiles whose look differs from the one for all domains.
  tiles: Record<string, TileLook>
}

export const PREFS_KEY = 'ownpanel.sidebar.v1'

export const DEFAULT_PREFS: RailPrefs = { expanded: false, initialsForAll: false, tiles: {} }

function isLook(value: unknown): value is TileLook {
  return value === 'icon' || value === 'initials'
}

export function parsePrefs(raw: string | null): RailPrefs {
  if (!raw) return DEFAULT_PREFS
  try {
    const value: unknown = JSON.parse(raw)
    if (!value || typeof value !== 'object') return DEFAULT_PREFS
    const record = value as Record<string, unknown>
    const tiles: Record<string, TileLook> = {}
    if (record.tiles && typeof record.tiles === 'object') {
      for (const [domain, look] of Object.entries(record.tiles as Record<string, unknown>)) {
        if (isLook(look)) tiles[domain] = look
      }
    }
    return { expanded: record.expanded === true, initialsForAll: record.initialsForAll === true, tiles }
  } catch {
    return DEFAULT_PREFS
  }
}

export function globalLook(prefs: RailPrefs): TileLook {
  return prefs.initialsForAll ? 'initials' : 'icon'
}

export function lookOf(prefs: RailPrefs, domain: string): TileLook {
  return prefs.tiles[domain] ?? globalLook(prefs)
}

export function withTileLook(prefs: RailPrefs, domain: string, look: TileLook): RailPrefs {
  const tiles = { ...prefs.tiles }
  if (look === globalLook(prefs)) delete tiles[domain]
  else tiles[domain] = look
  return { ...prefs, tiles }
}

// Switching the look of all domains keeps the tiles that were set apart on purpose, except
// those that now match it anyway.
export function withInitialsForAll(prefs: RailPrefs, initialsForAll: boolean): RailPrefs {
  const next = { ...prefs, initialsForAll }
  const look = globalLook(next)
  return { ...next, tiles: Object.fromEntries(Object.entries(prefs.tiles).filter(([, own]) => own !== look)) }
}

export function withExpanded(prefs: RailPrefs, expanded: boolean): RailPrefs {
  return { ...prefs, expanded }
}

function storage(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

const listeners = new Set<() => void>()
let cachedRaw: string | null | undefined
let cached: RailPrefs = DEFAULT_PREFS

function readRaw(): string | null {
  try {
    return storage()?.getItem(PREFS_KEY) ?? null
  } catch {
    return null
  }
}

export function readPrefs(): RailPrefs {
  const raw = readRaw()
  if (raw !== cachedRaw) {
    cachedRaw = raw
    cached = parsePrefs(raw)
  }
  return cached
}

export function updatePrefs(change: (prefs: RailPrefs) => RailPrefs) {
  const next = change(readPrefs())
  try {
    storage()?.setItem(PREFS_KEY, JSON.stringify(next))
  } catch {
    // Storage may be refused (private windows, full quota); the choice then lasts until reload.
  }
  cachedRaw = readRaw()
  cached = next
  listeners.forEach((listener) => listener())
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  const onStorage = (event: StorageEvent) => {
    if (event.key === PREFS_KEY || event.key === null) listener()
  }
  window.addEventListener('storage', onStorage)
  return () => {
    listeners.delete(listener)
    window.removeEventListener('storage', onStorage)
  }
}

export function useRailPrefs(): RailPrefs {
  return useSyncExternalStore(subscribe, readPrefs, () => DEFAULT_PREFS)
}
