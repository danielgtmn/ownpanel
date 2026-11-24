import type { Connection, Zone, ZoneList, ZoneProblem } from './types.ts'

export function problemMessage(problem: ZoneProblem): string {
  const reason = problem.error.trim()
  const base = `Couldn't load the domains from ${problem.connection_name}.`
  return reason ? `${base} ${reason}` : base
}

export type ZoneRow = {
  key: string
  connectionId: number
  connectionName: string
  name: string
  readOnly: boolean
}

export type ZonesView = {
  rows: ZoneRow[]
  notices: { key: number; text: string }[]
}

export function describeZones(list: ZoneList): ZonesView {
  const rows = [...list.zones]
    .sort((a, b) => a.name.localeCompare(b.name) || a.connection_name.localeCompare(b.connection_name))
    .map((zone: Zone) => ({
      key: `${zone.connection_id}/${zone.name}`,
      connectionId: zone.connection_id,
      connectionName: zone.connection_name,
      name: zone.name,
      readOnly: zone.read_only,
    }))
  const notices = list.problems.map((problem) => ({ key: problem.connection_id, text: problemMessage(problem) }))
  return { rows, notices }
}

export function findZone(list: ZoneList, connectionId: number, name: string): Zone | undefined {
  return list.zones.find((zone) => zone.connection_id === connectionId && zone.name === name)
}

export function parseConnectionId(segment: string | undefined): number | null {
  if (!segment || !/^[1-9]\d*$/.test(segment)) return null
  return Number(segment)
}

export const providerLabel = (provider: Connection['provider']): string => (provider === 'hetzner' ? 'Hetzner' : provider)

export function isOwnConnection(connection: Connection, user: { name: string; role: string }): boolean {
  return user.role !== 'admin' || connection.owner === user.name
}

export function zoneCountLabel(count: number | null): string | null {
  if (count === null) return null
  return count === 1 ? '1 domain' : `${count} domains`
}

export type ConnectionHealth = { tone: 'ok' | 'problem' | 'unknown'; text: string }

export function connectionHealth(connection: Connection, formatDate: (value: string) => string): ConnectionHealth {
  if (connection.last_error.trim()) return { tone: 'problem', text: `Problem: ${connection.last_error.trim()}` }
  if (connection.last_checked_at) return { tone: 'ok', text: `Working. Checked ${formatDate(connection.last_checked_at)}` }
  return { tone: 'unknown', text: 'Not checked yet' }
}

export function validateConnection(name: string, token: string): { name?: string; token?: string } {
  const errors: { name?: string; token?: string } = {}
  if (!name.trim()) errors.name = 'Enter a name to recognize this connection.'
  else if (name.trim().length > 64) errors.name = 'Use at most 64 characters.'
  if (!token.trim()) errors.token = 'Paste the API token from Hetzner.'
  return errors
}
