import { encodeSegment, request } from '../../shared/api/index.ts'
import type { Connection, NewConnection, RecordBody, RecordSet, Zone, ZoneList } from './types.ts'

const connectionPath = (id: number) => `/dns/connections/${encodeSegment(String(id))}`

export const recordsPath = (connectionId: number, zone: string) =>
  `${connectionPath(connectionId)}/zones/${encodeSegment(zone)}/records`

export const recordPath = (connectionId: number, zone: string, name: string, type: string) =>
  `${recordsPath(connectionId, zone)}/${encodeSegment(name)}/${encodeSegment(type)}`

export function listConnections(): Promise<Connection[]> {
  return request<Connection[]>('/dns/connections')
}

export function createConnection(connection: NewConnection): Promise<Connection> {
  return request<Connection>('/dns/connections', { method: 'POST', body: connection })
}

export function checkConnection(id: number): Promise<Connection> {
  return request<Connection>(`${connectionPath(id)}/check`, { method: 'POST' })
}

export function deleteConnection(id: number): Promise<void> {
  return request<void>(connectionPath(id), { method: 'DELETE' })
}

export async function listZones(): Promise<ZoneList> {
  const result = await request<Partial<ZoneList>>('/dns/zones')
  const zones = (result.zones ?? []).map((zone) => ({ ...zone, nameservers: zone.nameservers ?? [] }))
  return { zones, problems: result.problems ?? [] }
}

export async function createZone(connectionId: number, name: string): Promise<Zone> {
  const zone = await request<Zone>(`${connectionPath(connectionId)}/zones`, { method: 'POST', body: { name } })
  return { ...zone, nameservers: zone.nameservers ?? [] }
}

export function deleteZone(connectionId: number, zone: string, confirm: string): Promise<void> {
  return request<void>(`${connectionPath(connectionId)}/zones/${encodeSegment(zone)}`, {
    method: 'DELETE',
    body: { confirm },
  })
}

export async function listRecords(connectionId: number, zone: string): Promise<RecordSet[]> {
  return (await request<RecordSet[] | null>(recordsPath(connectionId, zone))) ?? []
}

export function saveRecord(
  connectionId: number,
  zone: string,
  name: string,
  type: string,
  body: RecordBody,
): Promise<RecordSet> {
  return request<RecordSet>(recordPath(connectionId, zone, name, type), { method: 'PUT', body })
}

export function deleteRecord(connectionId: number, zone: string, name: string, type: string): Promise<void> {
  return request<void>(recordPath(connectionId, zone, name, type), { method: 'DELETE' })
}
