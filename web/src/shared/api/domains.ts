import { request } from './client.ts'
import { encodeSegment } from './path.ts'

export type DomainState = 'points_here' | 'points_elsewhere' | 'no_record' | 'lookup_failed'

export type DomainZone = {
  connection_id: number
  connection_name: string
  zone: string
  record_name: string
}

export type DomainStatus = {
  domain: string
  state: DomainState
  found: string[]
  zone: DomainZone | null
}

export type ChangedRecord = {
  name: string
  type: string
  ttl: number | null
  values: string[]
  locked: boolean
}

const domainsPath = (site: string) => `/sites/${encodeSegment(site)}/domains`

export function getDomainStatuses(site: string): Promise<DomainStatus[]> {
  return request<DomainStatus[]>(`${domainsPath(site)}/status`)
}

export async function pointDomain(site: string, domain: string): Promise<ChangedRecord[]> {
  const result = await request<{ changed: ChangedRecord[] | null }>(
    `${domainsPath(site)}/${encodeSegment(domain)}/point`,
    { method: 'POST' },
  )
  return result.changed ?? []
}

export async function getServerAddresses(): Promise<string[]> {
  const result = await request<{ ips: string[] | null }>('/server')
  return result.ips ?? []
}
