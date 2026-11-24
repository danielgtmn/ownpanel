import type { DomainState } from '../api/index.ts'
import { normalizeHost, registrableDomain } from './suffix.ts'

export type SourceSite = { name: string; domains: string[]; live: boolean }
export type SourceZone = { connectionId: number; connectionName: string; name: string }
export type SourceRedirect = { id: number; source: string; target: string }

export type DomainSources = {
  sites: SourceSite[]
  zones: SourceZone[]
  redirects: SourceRedirect[]
  panelDomain: string | null
}

export type FamilySite = {
  name: string
  live: boolean
  domains: string[]
  otherFamilies: string[]
}

export type Family = {
  domain: string
  sites: FamilySite[]
  zones: SourceZone[]
  redirects: SourceRedirect[]
  panelDomain: string | null
  servedDomains: string[]
}

export type FamilyStatus = 'ok' | 'attention' | 'pending' | 'idle'

export type PointStates = Record<string, DomainState>

function familyOf(families: Map<string, Family>, host: string): Family | null {
  const domain = registrableDomain(host)
  if (!domain) return null
  let family = families.get(domain)
  if (!family) {
    family = { domain, sites: [], zones: [], redirects: [], panelDomain: null, servedDomains: [] }
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

  if (sources.panelDomain) {
    const host = normalizeHost(sources.panelDomain)
    const family = familyOf(families, host)
    if (family) {
      family.panelDomain = host
      addServed(family, host)
    }
  }

  return [...families.values()]
    .map((family) => ({
      ...family,
      sites: [...family.sites].sort((a, b) => a.name.localeCompare(b.name)),
      zones: [...family.zones].sort((a, b) => a.connectionName.localeCompare(b.connectionName)),
      redirects: [...family.redirects].sort((a, b) => a.source.localeCompare(b.source)),
      servedDomains: [...family.servedDomains].sort(),
    }))
    .sort((a, b) => a.domain.localeCompare(b.domain))
}

export function findFamily(families: Family[], domain: string): Family | undefined {
  const wanted = normalizeHost(domain)
  return families.find((family) => family.domain === wanted)
}

export function familyStatus(family: Family, states: PointStates): FamilyStatus {
  if (family.sites.some((site) => !site.live)) return 'attention'
  if (family.servedDomains.length === 0) return 'idle'
  let pending = false
  for (const host of family.servedDomains) {
    const state = states[host]
    if (state === 'points_elsewhere' || state === 'no_record') return 'attention'
    if (state !== 'points_here') pending = true
  }
  return pending ? 'pending' : 'ok'
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
  if (family.panelDomain) parts.push('Panel address')
  parts.push(family.zones.length > 0 ? 'DNS connected' : 'DNS not connected')
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
