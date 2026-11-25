import { useMemo, useSyncExternalStore } from 'react'

export type Route = { segments: string[]; query: URLSearchParams }

export function parseHash(hash: string): string[] {
  const path = hash.replace(/^#/, '').split('?')[0]
  return path
    .split('/')
    .filter(Boolean)
    .map((segment) => {
      try {
        return decodeURIComponent(segment)
      } catch {
        return segment
      }
    })
}

export function parseQuery(hash: string): URLSearchParams {
  const index = hash.indexOf('?')
  return new URLSearchParams(index === -1 ? '' : hash.slice(index + 1))
}

export function parseRoute(hash: string): Route {
  return { segments: parseHash(hash), query: parseQuery(hash) }
}

export function hrefFor(...segments: string[]): string {
  return '#/' + segments.map(encodeURIComponent).join('/')
}

export function hrefWithQuery(segments: string[], query: Record<string, string>): string {
  const search = new URLSearchParams(query).toString()
  return hrefFor(...segments) + (search ? '?' + search : '')
}

export function navigate(...segments: string[]) {
  window.location.hash = hrefFor(...segments)
}

// Drops the query (like ?new) once a page has acted on it, without a new history entry.
export function clearQuery() {
  const hash = window.location.hash
  const index = hash.indexOf('?')
  if (index === -1) return
  window.history.replaceState(window.history.state, '', hash.slice(0, index) || '#/')
  window.dispatchEvent(new HashChangeEvent('hashchange'))
}

// Swaps the address without a history entry, so what was in it (like a one-time link) leaves the history.
export function replaceHash(hash: string) {
  const url = window.location.pathname + window.location.search + (hash && hash !== '#' ? hash : '')
  window.history.replaceState(window.history.state, '', url)
  window.dispatchEvent(new HashChangeEvent('hashchange'))
}

function subscribe(listener: () => void) {
  window.addEventListener('hashchange', listener)
  return () => window.removeEventListener('hashchange', listener)
}

function getHash() {
  return window.location.hash
}

export function useHash(): string {
  return useSyncExternalStore(subscribe, getHash)
}

export function usePathSegments(): string[] {
  const hash = useHash()
  return parseHash(hash)
}

export function useRoute(): Route {
  const hash = useHash()
  return useMemo(() => parseRoute(hash), [hash])
}

export function useQuery(): URLSearchParams {
  return useRoute().query
}
