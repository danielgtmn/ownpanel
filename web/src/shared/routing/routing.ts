import { useSyncExternalStore } from 'react'

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

export function hrefFor(...segments: string[]): string {
  return '#/' + segments.map(encodeURIComponent).join('/')
}

export function navigate(...segments: string[]) {
  window.location.hash = hrefFor(...segments)
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
