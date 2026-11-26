import { request } from '../../shared/api/index.ts'
import type { Run } from './source.ts'

export type SiteRuns = {
  site: string
  /** Newest first. */
  runs: Run[]
  /** How many runs the panel keeps per site. */
  kept: number
  publishing_available: boolean
}

export const getSiteRuns = (site: string) => request<SiteRuns>(`/sites/${encodeURIComponent(site)}/runs`)

/** How often the list looks for new pushes while the page is open. */
export const RUNS_REFRESH_MS = 20_000

/** What started a run, for people. */
export function runTrigger(run: Run): string {
  switch (run.trigger) {
    case 'push':
      return 'Push'
    case 'manual':
      return 'Started by hand'
    default:
      return 'Other'
  }
}

/** A run that may still change by itself. */
export function runActive(run: Run): boolean {
  return run.status === 'queued' || run.status === 'running'
}

/** Only links to GitHub's web pages: the address comes from the panel, but stays a link to a web page. */
export function commitLink(run: Run): string | null {
  const url = run.commit_url
  if (!url) return null
  try {
    const parsed = new URL(url)
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.toString() : null
  } catch {
    return null
  }
}

/** How long a finished run took, like "45 s" or "3 min 5 s"; null while it has not run. */
export function runDuration(run: Run): string | null {
  if (!run.started_at || !run.finished_at) return null
  const ms = new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()
  if (!Number.isFinite(ms) || ms < 0) return null
  const seconds = Math.round(ms / 1000)
  if (seconds < 60) return `${seconds} s`
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  if (minutes < 60) return rest ? `${minutes} min ${rest} s` : `${minutes} min`
  const hours = Math.floor(minutes / 60)
  const restMinutes = minutes % 60
  return restMinutes ? `${hours} h ${restMinutes} min` : `${hours} h`
}

/** The sentence under a run's status: why it waits, was replaced or failed. Null if there is nothing to add. */
export function runNote(run: Run): string | null {
  if (run.status === 'queued' && run.waiting) return 'Waits until building on the server is available.'
  // The status already says it was replaced.
  if (run.status === 'skipped' || !run.detail) return null
  const text = run.detail.trim()
  if (!text) return null
  const sentence = text.charAt(0).toUpperCase() + text.slice(1)
  return /[.!?]$/.test(sentence) ? sentence : `${sentence}.`
}
