import type { RecordName } from '../api/index.ts'
import type { DomainSources } from './families.ts'
import { normalizeHost, registrableDomain } from './suffix.ts'

// What an address of a domain is used for.
export type AddressUse =
  | { kind: 'site'; site: string; live: boolean }
  | { kind: 'redirect'; id: number; target: string }
  | { kind: 'none' }

// wildcard: a DNS record like *.example.com that answers for every name without its own record.
export type Address = { host: string; wildcard: boolean; use: AddressUse }

export type ZoneRecords = { zone: string; records: RecordName[] }

export type AddressState = 'website' | 'redirect' | 'unused' | 'wildcard'
export type AddressAction = 'add-website' | 'add-redirect' | 'add-to-website'

// Only these record types make a name reachable in a browser. MX, TXT, NS and the like don't.
const ADDRESS_TYPES = new Set(['A', 'AAAA', 'CNAME'])

// Record names are relative to their zone ("@" is the zone itself); a name ending in a dot is already full.
export function recordHost(zone: string, name: string): string {
  const relative = name.trim().toLowerCase()
  if (relative === '' || relative === '@') return normalizeHost(zone)
  if (relative.endsWith('.')) return normalizeHost(relative)
  return normalizeHost(`${relative}.${zone}`)
}

// Names with underscores (_dmarc, _acme-challenge, …) are for services, never for visitors.
const isServiceName = (host: string) => host.includes('_')
const isWildcard = (host: string) => host.startsWith('*.')

function compareHosts(domain: string) {
  const rank = (host: string) => (host === domain ? 0 : host === `www.${domain}` ? 1 : 2)
  return (a: string, b: string) => rank(a) - rank(b) || (a < b ? -1 : a > b ? 1 : 0)
}

export function collectAddresses(domain: string, sources: DomainSources, zones: ZoneRecords[] = []): Address[] {
  const own = normalizeHost(domain)
  const uses = new Map<string, AddressUse>()
  const hosts = new Set<string>([own, `www.${own}`])
  const add = (value: string, use?: AddressUse) => {
    const host = normalizeHost(value)
    if (!host || isServiceName(host) || registrableDomain(host.replace(/^\*\./, '')) !== own) return
    hosts.add(host)
    if (use && !uses.has(host)) uses.set(host, use)
  }

  const sites = [...sources.sites].sort((a, b) => a.name.localeCompare(b.name))
  for (const site of sites) {
    for (const host of site.domains) add(host, { kind: 'site', site: site.name, live: site.live })
  }
  for (const redirect of sources.redirects) {
    add(redirect.source, { kind: 'redirect', id: redirect.id, target: redirect.target })
  }
  for (const host of sources.pathRedirectHosts ?? []) add(host)
  for (const zone of zones) {
    for (const record of zone.records) {
      if (ADDRESS_TYPES.has(record.type.toUpperCase())) add(recordHost(zone.zone, record.name))
    }
  }

  return [...hosts].sort(compareHosts(own)).map((host) => ({
    host,
    wildcard: isWildcard(host),
    use: isWildcard(host) ? { kind: 'none' } : (uses.get(host) ?? { kind: 'none' }),
  }))
}

export function addressState(address: Address): AddressState {
  if (address.wildcard) return 'wildcard'
  if (address.use.kind === 'site') return 'website'
  if (address.use.kind === 'redirect') return 'redirect'
  return 'unused'
}

// Only unused addresses get actions; "add to a website" needs a website to add it to.
export function addressActions(address: Address, siteCount: number): AddressAction[] {
  if (addressState(address) !== 'unused') return []
  return siteCount > 0 ? ['add-website', 'add-redirect', 'add-to-website'] : ['add-website', 'add-redirect']
}

// Only addresses that serve something are checked: the server answers only for those, and a domain with many
// unused names doesn't start dozens of lookups.
export function checksOnLoad(address: Address): boolean {
  const state = addressState(address)
  return state === 'website' || state === 'redirect'
}

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

// Turns what was typed before ".<domain>" into a full address, checked like the server checks domains of sites.
export function subdomainHost(label: string, domain: string): { host: string } | { error: string } {
  const own = normalizeHost(domain)
  let value = label.trim().toLowerCase().replace(/\.$/, '')
  if (value.endsWith(`.${own}`)) value = value.slice(0, -own.length - 1)
  if (!value) return { error: 'Enter the subdomain, like shop.' }
  if (!/^[a-z0-9.-]+$/.test(value)) {
    return { error: 'Use only letters, digits and hyphens, and dots for deeper levels like shop.eu.' }
  }
  const parts = value.split('.')
  if (parts.some((part) => part === '')) return { error: 'Leave no empty part between dots.' }
  if (parts.some((part) => part.startsWith('-') || part.endsWith('-'))) {
    return { error: "A part can't start or end with a hyphen." }
  }
  if (parts.some((part) => part.length > 63)) return { error: 'Each part can be at most 63 characters long.' }
  if (!parts.every((part) => LABEL.test(part))) {
    return { error: 'Use only letters, digits and hyphens, and dots for deeper levels like shop.eu.' }
  }
  const host = `${value}.${own}`
  if (host.length > 253) return { error: 'This address is too long.' }
  return { host }
}
