import { formatReleaseDate, parseReleaseDate } from '../../shared/format/index.ts'
import type { Activity } from './types.ts'

export const LIMITS = [50, 100, 200] as const

export function nextLimit(limit: number): number | null {
  return LIMITS.find((candidate) => candidate > limit) ?? null
}

type Entry = Pick<Activity, 'actor' | 'action' | 'target'>

function about(target: string, withTarget: (target: string) => string, without: string): string {
  return target ? withTarget(target) : without
}

const VIA_TOKEN = / via token (.+)$/

export function describeActor(actor: string): string {
  if (!actor) return 'Someone'
  const match = VIA_TOKEN.exec(actor)
  if (!match) return actor
  return `${actor.slice(0, match.index)} (with the access token “${match[1]}”)`
}

const ROLES: Record<string, string> = { admin: 'Administrator', member: 'Member' }

function describeRole(role: string): string {
  return ROLES[role] ?? role
}

export function describeDetail({ action, detail }: Pick<Activity, 'action' | 'detail'>): string {
  if (!detail) return ''
  switch (action) {
    case 'site.publish':
    case 'site.make-live':
      return parseReleaseDate(detail) ? `Version from ${formatReleaseDate(detail)}` : detail
    case 'site.create':
    case 'site.domains':
      return `Domains: ${detail}`
    case 'user.create':
      return detail.replace(/^role (\w+)$/, (_, role: string) => `Role: ${describeRole(role)}`)
    case 'user.update':
      return detail
        .split(', ')
        .map((part) =>
          part === 'password changed'
            ? 'New password'
            : part.replace(/^role (\w+)$/, (_, role: string) => `Role: ${describeRole(role)}`),
        )
        .join(' · ')
    case 'token.create': {
      const match = /^(read|write) access, (?:all sites|sites (.+))$/.exec(detail)
      if (!match) return detail
      const rights = match[1] === 'read' ? 'Read only' : 'Read and change'
      return `${rights} · ${match[2] ? `Only ${match[2]}` : 'All sites'}`
    }
    case 'setup':
      return ''
    default:
      return detail
  }
}

export function describeActivity({ actor, action, target }: Entry): string {
  const who = describeActor(actor)
  switch (action) {
    case 'site.create':
      return about(target, (t) => `${who} created the site ${t}`, `${who} created a site`)
    case 'site.domains':
      return about(target, (t) => `${who} changed the domains of ${t}`, `${who} changed the domains of a site`)
    case 'site.publish':
      return about(target, (t) => `${who} published new files for ${t}`, `${who} published new files for a site`)
    case 'site.make-live':
      return about(target, (t) => `${who} made another version of ${t} live`, `${who} made another version of a site live`)
    case 'site.delete':
      return about(target, (t) => `${who} removed the site ${t}`, `${who} removed a site`)
    case 'user.create':
      return about(target, (t) => `${who} added ${t}`, `${who} added a person`)
    case 'user.update':
      return about(target, (t) => `${who} changed the account of ${t}`, `${who} changed a person's account`)
    case 'user.delete':
      return about(target, (t) => `${who} removed ${t}`, `${who} removed a person`)
    case 'token.create':
      return about(target, (t) => `${who} created the access token ${t}`, `${who} created an access token`)
    case 'token.delete':
      return about(target, (t) => `${who} deleted the access token ${t}`, `${who} deleted an access token`)
    case 'account.password':
      return `${who} changed their password`
    case 'account.login':
      return `${who} signed in`
    case 'setup':
      return `${who} set up the panel`
    default:
      return about(target, (t) => `${who} made a change to ${t}`, `${who} made a change`)
  }
}
