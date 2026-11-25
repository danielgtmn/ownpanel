import type { ChangedRecord, DomainStatus } from '../api/index.ts'

export type StatusTone = 'positive' | 'warning' | 'neutral'

export function statusText(status: Pick<DomainStatus, 'state'>): string {
  switch (status.state) {
    case 'points_here':
      return 'Points to this server'
    case 'points_elsewhere':
      return 'Points somewhere else'
    case 'no_record':
      return 'No address record yet'
    case 'lookup_failed':
      return 'Could not be checked'
  }
}

export function statusTone(status: Pick<DomainStatus, 'state'>): StatusTone {
  if (status.state === 'points_here') return 'positive'
  if (status.state === 'lookup_failed') return 'neutral'
  return 'warning'
}

/** The addresses a domain points to instead of this server, as a sentence; empty when there's nothing to say. */
export function foundText(status: Pick<DomainStatus, 'state' | 'found'>): string {
  if (status.state !== 'points_elsewhere' || status.found.length === 0) return ''
  return `Right now it points to ${status.found.join(', ')}.`
}

export type DomainAction = 'none' | 'point' | 'manual'

export function statusAction(status: Pick<DomainStatus, 'state' | 'zone'>): DomainAction {
  if (status.state === 'points_here') return 'none'
  return status.zone ? 'point' : 'manual'
}

function recordLabel(record: ChangedRecord, zone: string | undefined): string {
  const name = record.name === '@' || !zone ? (zone ?? record.name) : `${record.name}.${zone}`
  return `${record.type} ${name} → ${record.values.join(', ')}`
}

export function pointedMessage(domain: string, changed: ChangedRecord[], zone: string | undefined): string {
  if (changed.length === 0) return `${domain} already points to this server.`
  const details = changed.map((record) => recordLabel(record, zone)).join('; ')
  return `${domain} now points to this server (${details}). DNS changes can take a few minutes to be visible.`
}
