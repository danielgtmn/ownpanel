import type { AvailableRelease, Channel, UpdateInfo, UpdateStatus } from './updatesApi.ts'

export const CHANNELS: Channel[] = ['stable', 'nightly']

export const POLL_INTERVAL = 3000

export function channelName(channel: Channel | ''): string {
  if (channel === 'nightly') return 'Nightly builds'
  if (channel === 'stable') return 'Stable releases'
  return 'Unknown channel'
}

export function channelHint(channel: Channel): string {
  return channel === 'stable'
    ? 'Tested versions, released every now and then. Recommended.'
    : 'A new build after every change. For trying out what is coming; things may break.'
}

export function isBusy(status: UpdateStatus): boolean {
  return status.state === 'requested' || status.state === 'running'
}

export function isFinished(status: UpdateStatus): boolean {
  return status.state === 'succeeded' || status.state === 'failed' || status.state === 'rolled_back'
}

const STEPS: Record<string, string> = {
  waiting: 'Waiting for the server to start the update…',
  checking: 'Looking for the new version…',
  downloading: 'Downloading and verifying the new version…',
  backing_up: 'Backing up your data…',
  installing: 'Installing. The panel restarts now…',
  verifying: 'Checking that the new version is running…',
  rolling_back: "The new version didn't start. Going back to the previous one…",
}

// What the progress box says. `unreachable` means the panel didn't answer, which is expected while it restarts.
export function progressText(status: UpdateStatus, unreachable: boolean): string {
  if (unreachable) return 'Restarting… the panel is back in a moment.'
  return STEPS[status.step] ?? (status.state === 'requested' ? STEPS.waiting : 'Updating…')
}

export function progressTitle(status: UpdateStatus): string {
  return status.to ? `Updating to ${status.to}` : 'Updating'
}

export type ResultNotice = { tone: 'positive' | 'warning'; title: string; detail: string }

export function resultNotice(status: UpdateStatus): ResultNotice | null {
  if (!isFinished(status)) return null
  switch (status.state) {
    case 'succeeded':
      return {
        tone: 'positive',
        title: status.from && status.from !== status.to ? `Updated from ${status.from} to ${status.to}` : 'Up to date',
        detail: '',
      }
    case 'rolled_back':
      return {
        tone: 'warning',
        title: `The update to ${status.to || 'the new version'} didn't work`,
        detail: `${status.from || 'The previous version'} was put back and is running. Your data was backed up before the update.`,
      }
    default:
      return {
        tone: 'warning',
        title: status.to ? `The update to ${status.to} failed` : 'The update failed',
        detail: status.message || 'Nothing was changed.',
      }
  }
}

export function channelRelease(info: UpdateInfo, channel: Channel): AvailableRelease | null {
  return info.available[channel]
}

// The question asked before installing; switching to an older version says so plainly.
export function updateQuestion(info: UpdateInfo, channel: Channel): { title: string; description: string; confirmLabel: string } {
  const release = channelRelease(info, channel)
  const target = release?.version ?? 'the newest version'
  const switching = channel !== info.installed.channel
  const offline = 'The panel and your sites are unavailable for a few seconds while it restarts. Your data is backed up first.'
  if (switching && release?.older) {
    return {
      title: `Switch to ${channelName(channel).toLowerCase()} and go back to ${target}?`,
      description: `${target} is older than the installed ${info.installed.version}. Features added since may disappear, and data changed by the newer version may not work with it. ${offline}`,
      confirmLabel: `Install the older ${target}`,
    }
  }
  if (switching) {
    return {
      title: `Switch to ${channelName(channel).toLowerCase()}?`,
      description: `This installs ${target}. ${channelHint(channel)} ${offline}`,
      confirmLabel: `Switch and install ${target}`,
    }
  }
  return {
    title: `Update to ${target}?`,
    description: offline,
    confirmLabel: `Update to ${target}`,
  }
}

export function splitWindow(window: string): [string, string] {
  const [from = '03:00', to = '05:00'] = window.split('-')
  return [from, to]
}

export function windowProblem(from: string | null, to: string | null): string | null {
  if (!from || !to) return 'Enter both times.'
  if (from === to) return 'The start and the end must differ.'
  return null
}

// The web app comes with the panel: a different installed version means this page is outdated.
export function needsReload(loadedVersion: string | null, info: UpdateInfo): boolean {
  return loadedVersion !== null && info.installed.version !== loadedVersion && !isBusy(info.status)
}

const relative = new Intl.RelativeTimeFormat('en', { numeric: 'always' })

// "Last checked 12 minutes ago"; null before the panel ever looked.
export function lastCheckedText(checkedAt: string, now: Date): string | null {
  const at = new Date(checkedAt)
  if (!checkedAt || Number.isNaN(at.getTime())) return null
  const seconds = Math.max(0, Math.round((now.getTime() - at.getTime()) / 1000))
  if (seconds < 60) return 'Last checked just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `Last checked ${relative.format(-minutes, 'minute')}`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `Last checked ${relative.format(-hours, 'hour')}`
  return `Last checked ${relative.format(-Math.floor(hours / 24), 'day')}`
}

export type CheckResult = { kind: 'failed' } | { kind: 'update'; version: string } | { kind: 'current' }

// What a check found for the installed channel.
export function checkResult(info: UpdateInfo): CheckResult {
  if (info.check_failed) return { kind: 'failed' }
  const channel = info.installed.channel
  const release = channel ? info.available[channel] : null
  if (release && release.version !== info.installed.version && !release.older) {
    return { kind: 'update', version: release.version }
  }
  return { kind: 'current' }
}
