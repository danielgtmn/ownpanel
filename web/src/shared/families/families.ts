import type { DomainState } from '../api/index.ts'
import { normalizeHost, registrableDomain } from './suffix.ts'

// release is the live version's id, when known.
export type SourceSite = { name: string; domains: string[]; live: boolean; release?: string }
export type SourceZone = { connectionId: number; connectionName: string; name: string }
export type SourceRedirect = { id: number; source: string; target: string }
// A domain with e-mail turned on. active: its mail records point to this server, so it receives mail.
export type SourceMailDomain = { domain: string; active: boolean }

export type DomainSources = {
  sites: SourceSite[]
  zones: SourceZone[]
  redirects: SourceRedirect[]
  // Domains with redirects for single paths. They count as addresses of their domain, nothing more.
  pathRedirectHosts?: string[]
  // Missing when the signed-in person may not see e-mail (for example a token limited to sites).
  mailDomains?: SourceMailDomain[]
}

export type FamilySite = {
  name: string
  live: boolean
  release?: string
  domains: string[]
  otherFamilies: string[]
}

export type Family = {
  domain: string
  sites: FamilySite[]
  zones: SourceZone[]
  redirects: SourceRedirect[]
  servedDomains: string[]
  mailDomains: SourceMailDomain[]
}

export type FamilyStatus = 'ok' | 'attention' | 'pending' | 'idle'

export type PointStates = Record<string, DomainState>

function familyOf(families: Map<string, Family>, host: string): Family | null {
  const domain = registrableDomain(host)
  if (!domain) return null
  let family = families.get(domain)
  if (!family) {
    family = { domain, sites: [], zones: [], redirects: [], servedDomains: [], mailDomains: [] }
    families.set(domain, family)
  }
  return family
}

function addServed(family: Family, host: string) {
  if (!family.servedDomains.includes(host)) family.servedDomains.push(host)
}

export function buildFamilies(sources: DomainSources): Family[] {
  const families = new Map<string, Family>()

  for (const site of sources.sites) {
    const hosts = [...new Set(site.domains.map(normalizeHost).filter(Boolean))]
    const domains = [...new Set(hosts.map(registrableDomain))]
    for (const domain of domains) {
      const family = familyOf(families, domain)
      if (!family) continue
      const own = hosts.filter((host) => registrableDomain(host) === domain)
      own.forEach((host) => addServed(family, host))
      family.sites.push({
        name: site.name,
        live: site.live,
        release: site.release,
        domains: own,
        otherFamilies: domains.filter((other) => other !== domain),
      })
    }
  }

  for (const zone of sources.zones) {
    familyOf(families, zone.name)?.zones.push(zone)
  }

  for (const redirect of sources.redirects) {
    const source = normalizeHost(redirect.source)
    const family = familyOf(families, source)
    if (!family) continue
    family.redirects.push({ ...redirect, source })
    addServed(family, source)
  }

  for (const mail of sources.mailDomains ?? []) {
    const domain = normalizeHost(mail.domain)
    familyOf(families, domain)?.mailDomains.push({ ...mail, domain })
  }

  return [...families.values()]
    .map((family) => ({
      ...family,
      sites: [...family.sites].sort((a, b) => a.name.localeCompare(b.name)),
      zones: [...family.zones].sort((a, b) => a.connectionName.localeCompare(b.connectionName)),
      redirects: [...family.redirects].sort((a, b) => a.source.localeCompare(b.source)),
      servedDomains: [...family.servedDomains].sort(),
      mailDomains: [...family.mailDomains].sort((a, b) => a.domain.localeCompare(b.domain)),
    }))
    .sort((a, b) => a.domain.localeCompare(b.domain))
}

export function findFamily(families: Family[], domain: string): Family | undefined {
  const wanted = normalizeHost(domain)
  return families.find((family) => family.domain === wanted)
}

export function familyStatus(family: Family, states: PointStates): FamilyStatus {
  if (family.sites.some((site) => !site.live)) return 'attention'
  // A mail domain waits until its mail records point here; that needs someone to act.
  if (family.mailDomains.some((mail) => !mail.active)) return 'attention'
  if (family.servedDomains.length === 0) return family.mailDomains.length > 0 ? 'ok' : 'idle'
  let pending = false
  for (const host of family.servedDomains) {
    const state = states[host]
    if (state === 'points_elsewhere' || state === 'no_record') return 'attention'
    if (state !== 'points_here') pending = true
  }
  return pending ? 'pending' : 'ok'
}

/** What makes a family need attention, as one sentence; empty when nothing does. */
export function attentionText(family: Family, states: PointStates): string {
  const parts: string[] = []
  const empty = family.sites.filter((site) => !site.live).map((site) => site.name)
  if (empty.length === 1) parts.push(`The site ${empty[0]} has no content yet`)
  else if (empty.length > 1) parts.push(`${empty.length} sites have no content yet`)
  const away = family.servedDomains.filter((host) => {
    const state = states[host]
    return state === 'points_elsewhere' || state === 'no_record'
  })
  if (away.length === 1) parts.push(`${away[0]} doesn't point to this server yet`)
  else if (away.length > 1) parts.push(`${away.length} addresses don't point to this server yet`)
  const waiting = family.mailDomains.filter((mail) => !mail.active).map((mail) => mail.domain)
  if (waiting.length === 1) parts.push(`e-mail for ${waiting[0]} waits for its mail records`)
  else if (waiting.length > 1) parts.push(`e-mail for ${waiting.length} domains waits for its mail records`)
  // No capital letter added: a sentence may start with a domain, which is written as it is.
  return parts.length === 0 ? '' : `${parts.join(', and ')}.`
}

export const STATUS_TEXT: Record<FamilyStatus, string> = {
  ok: 'everything works',
  attention: 'needs attention',
  pending: 'still checking',
  idle: 'nothing runs here yet',
}

const plural = (count: number, one: string, many: string) => `${count} ${count === 1 ? one : many}`

export function familySummary(family: Family): string {
  const parts: string[] = []
  if (family.sites.length > 0) parts.push(plural(family.sites.length, 'site', 'sites'))
  if (family.redirects.length > 0) parts.push(plural(family.redirects.length, 'redirect', 'redirects'))
  if (family.mailDomains.length > 0) parts.push('E-mail')
  parts.push(family.zones.length > 0 ? 'DNS managed here' : 'DNS managed elsewhere')
  return parts.join(' · ')
}

export function overviewSummary(sources: DomainSources, families: Family[]): string {
  return [
    plural(families.length, 'domain', 'domains'),
    plural(sources.sites.length, 'site', 'sites'),
    plural(sources.redirects.length, 'redirect', 'redirects'),
  ].join(' · ')
}

export function initialsOf(domain: string): string {
  const name = domain.split('.')[0] ?? domain
  const letters = name.replace(/[^a-z0-9]/gi, '')
  if (!letters) return domain.slice(0, 2).toUpperCase()
  return letters.charAt(0).toUpperCase() + letters.slice(1, 2)
}

export function hueOf(domain: string): number {
  let hash = 0
  for (const char of domain) hash = (hash * 31 + char.charCodeAt(0)) >>> 0
  return hash % 6
}
