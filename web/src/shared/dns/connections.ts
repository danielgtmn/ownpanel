import type { Connection } from './types.ts'

export const providerLabel = (provider: Connection['provider']): string => (provider === 'hetzner' ? 'Hetzner' : provider)

// Admins see everyone's accounts but can use only their own.
export function isOwnConnection(connection: Connection, user: { name: string; role: string }): boolean {
  return user.role !== 'admin' || connection.owner === user.name
}

export function ownConnections(connections: Connection[], user: { name: string; role: string }): Connection[] {
  return connections.filter((connection) => isOwnConnection(connection, user))
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
  if (!name.trim()) errors.name = 'Enter a name to recognize this account.'
  else if (name.trim().length > 64) errors.name = 'Use at most 64 characters.'
  if (!token.trim()) errors.token = 'Paste the API token from Hetzner.'
  return errors
}
