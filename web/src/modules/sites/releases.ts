import { formatReleaseDate, parseReleaseDate } from '../../shared/format/index.ts'
import type { Site } from './types.ts'

export { formatReleaseDate, parseReleaseDate }

export function releaseTimestamp(id: string): string | undefined {
  return parseReleaseDate(id)?.toISOString()
}

export function newestFirst(releases: string[]): string[] {
  return [...releases].reverse()
}

export function hasContent(site: Site): boolean {
  return site.current_release !== ''
}
