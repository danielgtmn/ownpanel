import { request } from './client.ts'
import { encodeSegment } from './path.ts'

export async function listSiteNames(): Promise<string[]> {
  const sites = await request<{ name: string }[]>('/sites')
  return sites.map((site) => site.name)
}

// Replaces the site's domains; pass the ones it keeps as well.
export async function saveSiteDomains(name: string, domains: string[]): Promise<void> {
  await request<unknown>(`/sites/${encodeSegment(name)}`, { method: 'PUT', body: { domains } })
}
