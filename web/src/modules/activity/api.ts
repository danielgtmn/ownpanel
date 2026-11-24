import { request } from '../../shared/api/index.ts'
import type { Activity } from './types.ts'

export function listActivity(limit: number): Promise<Activity[]> {
  return request<Activity[]>(`/activity?limit=${limit}`)
}
