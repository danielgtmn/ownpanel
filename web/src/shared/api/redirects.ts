import { request } from './client.ts'
import { encodeSegment } from './path.ts'

export type RedirectCode = 301 | 302 | 307 | 308

export type RedirectInput = {
  source_domain: string
  source_path: string
  target: string
  code: RedirectCode
  keep_path: boolean
  keep_query: boolean
}

export type Redirect = RedirectInput & {
  id: number
  owner: string
  created_at: string
}

const redirectPath = (id: number) => `/redirects/${encodeSegment(String(id))}`

export async function listRedirects(): Promise<Redirect[]> {
  return (await request<Redirect[] | null>('/redirects')) ?? []
}

export async function listSiteRedirects(site: string): Promise<Redirect[]> {
  return (await request<Redirect[] | null>(`/sites/${encodeSegment(site)}/redirects`)) ?? []
}

export function createRedirect(input: RedirectInput): Promise<Redirect> {
  return request<Redirect>('/redirects', { method: 'POST', body: input })
}

export function updateRedirect(id: number, input: RedirectInput): Promise<Redirect> {
  return request<Redirect>(redirectPath(id), { method: 'PUT', body: input })
}

export function deleteRedirect(id: number): Promise<void> {
  return request<void>(redirectPath(id), { method: 'DELETE' })
}
