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
  update_available: boolean
  auto: { mode: AutoMode; window: string }
  status: UpdateStatus
  supported: boolean
}

export type UpdateTarget = { channel: Channel; allow_downgrade?: boolean }

export function getUpdates(signal?: AbortSignal): Promise<UpdateInfo> {
  return request<UpdateInfo>('/updates', { signal })
}

export function requestUpdate(target: UpdateTarget): Promise<UpdateInfo> {
  return request<UpdateInfo>('/updates', { method: 'POST', body: target })
}

export function setAutoUpdate(auto: UpdateInfo['auto']): Promise<UpdateInfo> {
  return request<UpdateInfo>('/updates/auto', { method: 'PUT', body: auto })
}
