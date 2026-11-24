import { request } from './client.ts'

export async function listSiteNames(): Promise<string[]> {
  const sites = await request<{ name: string }[]>('/sites')
  return sites.map((site) => site.name)
}
