import type { Zone } from './types.ts'

export type AccountGroup = { connectionId: string; connectionName: string; zones: Zone[] }

export const zoneKey = (zone: Pick<Zone, 'connection_id' | 'name'>) => `${zone.connection_id}/${zone.name}`

// The domains to choose from, one group per account, accounts and domains by name. Accounts with the same name keep
// the order they came in (ids are opaque, so they are not sorted by).
export function groupByAccount(zones: Zone[]): AccountGroup[] {
  const groups = new Map<string, AccountGroup>()
  for (const zone of zones) {
    let group = groups.get(zone.connection_id)
    if (!group) {
      group = { connectionId: zone.connection_id, connectionName: zone.connection_name, zones: [] }
      groups.set(zone.connection_id, group)
    }
    group.zones.push(zone)
  }
  return [...groups.values()]
    .sort((a, b) => a.connectionName.localeCompare(b.connectionName))
    .map((group) => ({ ...group, zones: [...group.zones].sort((a, b) => a.name.localeCompare(b.name)) }))
}

export function zonesNamed(zones: Zone[], domain: string): Zone[] {
  const name = domain.trim().replace(/\.$/, '').toLowerCase()
  return zones.filter((zone) => zone.name === name)
}

// Why a domain can't be taken out of the panel, or null when it can.
export function staysBecause(zone: Pick<Zone, 'name' | 'used_by'>): string | null {
  if (zone.used_by.length === 0) return null
  const uses = zone.used_by.join(', ')
  return `${zone.name} stays here while it is used by ${uses}.`
}
