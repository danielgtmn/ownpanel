import { request } from '../../shared/api/index.ts'

// Whether this server can build sites (GET /api/builds/status, administrators only).
export type BuildAvailability = {
  available: boolean
  // Why not: a fixed code. `detail` is the technical cause for administrators; it is not shown here.
  reason?: string
  detail?: string
  slots: number
  running: number
  waiting: number
  max_memory_bytes: number
  max_cpu_millis: number
  max_seconds: number
  workspace_bytes: number
  cache_bytes: number
  disk_bytes: number
  free_bytes: number
  images: string[]
}

export function getBuildAvailability(signal?: AbortSignal): Promise<BuildAvailability> {
  return request<BuildAvailability>('/builds/status', { signal })
}

export type BuildSummary = {
  tone: 'positive' | 'neutral' | 'warning'
  title: string
  text: string
}

const REASONS: Record<string, string> = {
  off: 'Building sites was turned off for this server. To turn it on, run the installer again with the option --builds.',
  system:
    "This server's operating system can't keep builds apart from the rest of the server. Building sites needs Debian 12 or Ubuntu 24.04, or newer.",
  runtime: 'The part of the server that keeps builds apart is missing or too old. Run the installer again.',
  install: 'Setting up builds failed when the panel was installed or updated. Run the installer again and look at what it reports.',
  limits: "The limits on memory, processor time and processes of a build can't be enforced on this server.",
  disk: "The disk space set aside for builds isn't there. The server may not have enough free space.",
  network:
    "Builds are paused: the protection that keeps them away from this server's own services and network isn't active. They resume on their own once it is back.",
  images: "The environments builds run in couldn't be downloaded yet. The server tries again every few minutes.",
  worker: "The service that runs builds isn't running.",
  not_configured: 'This panel was started without the service that runs builds.',
}

/** What the section says about the build environment. */
export function buildSummary(info: BuildAvailability): BuildSummary {
  if (info.available) {
    return { tone: 'positive', title: 'This server can build sites', text: limitsText(info) }
  }
  if (info.reason === 'preparing') {
    return {
      tone: 'neutral',
      title: 'Getting ready to build sites',
      text: 'After an install or an update this takes a few minutes.',
    }
  }
  return {
    tone: 'warning',
    title: "This server can't build sites",
    text: REASONS[info.reason ?? ''] ?? "The service that runs builds reports a problem. Run the installer again.",
  }
}

/** The limits every build gets, in words. */
export function limitsText(info: BuildAvailability): string {
  const parts = [
    `Up to ${info.slots} ${info.slots === 1 ? 'build' : 'builds'} at a time.`,
    `Each build runs apart from the panel and your other sites, with up to ${formatSize(info.max_memory_bytes)} of memory, ${formatSize(info.workspace_bytes)} of disk space and ${formatDuration(info.max_seconds)}.`,
  ]
  return parts.join(' ')
}

/** Builds that run or wait right now, or '' when there are none. */
export function activityText(info: BuildAvailability): string {
  if (!info.available || (info.running === 0 && info.waiting === 0)) return ''
  const running = `${info.running} running`
  return info.waiting > 0 ? `${running}, ${info.waiting} waiting` : running
}

export function formatSize(bytes: number): string {
  const gib = bytes / 1024 ** 3
  if (gib >= 1) return `${gib.toLocaleString('en', { maximumFractionDigits: 1 })} GB`
  return `${Math.round(bytes / 1024 ** 2).toLocaleString('en')} MB`
}

export function formatDuration(seconds: number): string {
  if (seconds % 3600 === 0) {
    const h = seconds / 3600
    return h === 1 ? 'an hour' : `${h} hours`
  }
  const m = Math.round(seconds / 60)
  return m === 1 ? 'a minute' : `${m} minutes`
}
