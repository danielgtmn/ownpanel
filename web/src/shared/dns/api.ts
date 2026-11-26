import { request } from '../api/client.ts'
import { encodeSegment } from '../api/path.ts'
import type { Connection, NewConnection, Zone, ZoneList } from './types.ts'

export const connectionPath = (id: string) => `/dns/connections/${encodeSegment(id)}`

const zonePath = (connectionId: string, zone: string) => `${connectionPath(connectionId)}/zones/${encodeSegment(zone)}`

export function listConnections(): Promise<Connection[]> {
  return request<Connection[]>('/dns/connections')
}

export function createConnection(connection: NewConnection): Promise<Connection> {
  return request<Connection>('/dns/connections', { method: 'POST', body: connection })
}

export function checkConnection(id: string): Promise<Connection> {
  return request<Connection>(`${connectionPath(id)}/check`, { method: 'POST' })
}

export function deleteConnection(id: string): Promise<void> {
  return request<void>(connectionPath(id), { method: 'DELETE' })
}

const zoneOf = (zone: Partial<Zone>): Zone =>
  ({ ...zone, nameservers: zone.nameservers ?? [], used_by: zone.used_by ?? [], adopted: zone.adopted ?? false }) as Zone

async function zoneList(path: string): Promise<ZoneList> {
  const result = await request<Partial<ZoneList>>(path)
  return { zones: (result.zones ?? []).map(zoneOf), problems: result.problems ?? [] }
}

/** The domains managed in the panel: chosen ones and those something uses. */
export function listZones(): Promise<ZoneList> {
  return zoneList('/dns/zones')
}

/** The domains of the connected accounts that are not managed in the panel yet. */
export function listAvailableZones(): Promise<ZoneList> {
  return zoneList('/dns/zones/available')
}

/** Starts managing a domain of a connected account in the panel; nothing changes at the provider. */
export async function adoptZone(connectionId: string, zone: string): Promise<Zone> {
  return zoneOf(await request<Zone>(`${zonePath(connectionId, zone)}/managed`, { method: 'PUT' }))
}

/** Stops managing a domain in the panel; it stays at the provider unchanged. */
export function releaseZone(connectionId: string, zone: string): Promise<void> {
  return request<void>(`${zonePath(connectionId, zone)}/managed`, { method: 'DELETE' })
}

/** Creates a domain at the provider; it is managed in the panel right away. */
export async function createZone(connectionId: string, name: string): Promise<Zone> {
  return zoneOf(await request<Zone>(`${connectionPath(connectionId)}/zones`, { method: 'POST', body: { name } }))
}

export function deleteZone(connectionId: string, zone: string, confirm: string): Promise<void> {
  return request<void>(zonePath(connectionId, zone), { method: 'DELETE', body: { confirm } })
}
