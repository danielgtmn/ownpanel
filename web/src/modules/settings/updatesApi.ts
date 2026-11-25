import { request } from '../../shared/api/index.ts'

export type Channel = 'stable' | 'nightly'
export type AutoMode = 'off' | Channel
export type UpdateState = 'idle' | 'requested' | 'running' | 'succeeded' | 'failed' | 'rolled_back'

export type AvailableRelease = {
  version: string
  published_at: string
  notes_url: string
  older: boolean
}

export type UpdateStatus = {
  state: UpdateState
  step: string
  message: string
  started_at: string
  finished_at: string
  from: string
  to: string
}

export type UpdateInfo = {
  installed: { version: string; channel: Channel | '' }
  available: { stable: AvailableRelease | null; nightly: AvailableRelease | null }
  check_failed: boolean
  // Why the last lookup failed; `available` then still holds the last result that worked.
  check_error: string
  // When the panel last looked for new versions (RFC 3339), '' if never.
  checked_at: string
  update_available: boolean
  auto: { mode: AutoMode; window: string }
  status: UpdateStatus
  supported: boolean
}

export type UpdateTarget = { channel: Channel; allow_downgrade?: boolean }

export function getUpdates(signal?: AbortSignal): Promise<UpdateInfo> {
  return request<UpdateInfo>('/updates', { signal })
}

// Looks for new versions now instead of using the last result. The panel looks at most once a minute.
export function checkForUpdates(): Promise<UpdateInfo> {
  return request<UpdateInfo>('/updates/check', { method: 'POST' })
}

export function requestUpdate(target: UpdateTarget): Promise<UpdateInfo> {
  return request<UpdateInfo>('/updates', { method: 'POST', body: target })
}

export function setAutoUpdate(auto: UpdateInfo['auto']): Promise<UpdateInfo> {
  return request<UpdateInfo>('/updates/auto', { method: 'PUT', body: auto })
}
