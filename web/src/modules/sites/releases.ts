import { formatReleaseDate, parseReleaseDate } from '../../shared/format/index.ts'
import type { Site, Version } from './types.ts'

export { formatReleaseDate, parseReleaseDate }

export function releaseTimestamp(id: string): string | undefined {
  return parseReleaseDate(id)?.toISOString()
}

export function newestFirst(releases: string[]): string[] {
  return [...releases].reverse()
}

/** The site's versions, newest first, also from a panel that only lists ids. */
export function siteVersions(site: Site): Version[] {
  if (site.versions) return site.versions
  return newestFirst(site.releases).map((id) => ({ id, live: id === site.current_release, pending: false, preview_url: '' }))
}

/** What a version is for visitors right now. */
export function versionState(version: Version): { text: string; tone: 'positive' | 'neutral' | 'warning' } {
  if (version.live) return { text: 'Live', tone: 'positive' }
  if (version.pull_request) return { text: `Preview of pull request #${version.pull_request}`, tone: 'neutral' }
  if (version.pending) return { text: 'Not live yet', tone: 'warning' }
  return { text: 'Not live', tone: 'neutral' }
}

export function hasContent(site: Site): boolean {
  return site.current_release !== ''
}
