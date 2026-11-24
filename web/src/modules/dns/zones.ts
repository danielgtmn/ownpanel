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

export function normalizeZoneName(value: string): string {
  return value.trim().replace(/\.$/, '').toLowerCase()
}

export function canConfirmDelete(typed: string, zone: string): boolean {
  const name = normalizeZoneName(zone)
  return name !== '' && normalizeZoneName(typed) === name
}

export function nameserverList(nameservers: string[] | null | undefined): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of nameservers ?? []) {
    const name = normalizeZoneName(raw)
    if (name && !seen.has(name)) {
      seen.add(name)
      out.push(name)
    }
  }
  return out
}

function splitTopLevel(text: string): string[] {
  const parts: string[] = []
  let depth = 0
  let start = 0
  for (let index = 0; index < text.length; index++) {
    const char = text[index]
    if (char === '(') depth++
    else if (char === ')') depth = Math.max(0, depth - 1)
    else if (char === ',' && depth === 0) {
      parts.push(text.slice(start, index))
      start = index + 1
    }
  }
  parts.push(text.slice(start))
  return parts.map((part) => part.trim()).filter(Boolean)
}

export function usesFromMessage(message: string): string[] {
  const match = /still in use by (.+?);\s*remove those first/i.exec(message)
  if (!match) return []
  return splitTopLevel(match[1]).map((use) => use.charAt(0).toUpperCase() + use.slice(1))
}

export function validateZoneName(value: string): string | null {
  const name = normalizeZoneName(value)
  if (!name) return 'Enter the domain, like example.com.'
  if (/^[a-z]+:\/\//i.test(name) || name.includes('/')) return 'Enter just the domain, like example.com, without https:// or a path.'
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(name) || name.includes(':')) return 'Enter a domain like example.com, not an IP address.'
  if (/[^ -~]/.test(name)) return 'This domain has special characters. Enter it in its xn-- form.'
  if (!/^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name)) {
    return `"${value.trim()}" isn't a valid domain. Enter one like example.com.`
  }
  return null
}
