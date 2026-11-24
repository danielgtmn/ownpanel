import { request } from '../../shared/api/index.ts'
import type { Site } from './types.ts'

const sitePath = (name: string) => `/sites/${encodeURIComponent(name)}`

export function listSites(): Promise<Site[]> {
  return request<Site[]>('/sites')
}

export function getSite(name: string): Promise<Site> {
  return request<Site>(sitePath(name))
}

export function saveSite(name: string, domains: string[]): Promise<Site> {
  return request<Site>(sitePath(name), { method: 'PUT', body: { domains } })
}

export function deleteSite(name: string): Promise<void> {
  return request<void>(sitePath(name), { method: 'DELETE' })
}

export function publishArchive(name: string, file: Blob): Promise<Site> {
  return request<Site>(`${sitePath(name)}/deployments`, { method: 'POST', rawBody: file })
}

export function makeLive(name: string, release: string): Promise<Site> {
  return request<Site>(`${sitePath(name)}/rollback`, { method: 'POST', body: { release } })
}
