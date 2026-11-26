import { listRecordNames, request, type DomainStatus } from '../../shared/api/index.ts'
import type { DomainSources, PointStates, SourceZone, ZoneRecords } from '../../shared/families/index.ts'

type SiteData = { name: string; domains: string[] | null; current_release: string }
type ZoneData = { zones?: { connection_id: string; connection_name: string; name: string }[] | null }
type RedirectData = { id: string; source_domain: string; source_path: string; target: string }
type MailDomainData = { domain: string; active: boolean }
type AddedDomainData = { name: string }

async function optional<T>(load: Promise<T>, fallback: T): Promise<T> {
  try {
    return await load
  } catch {
    return fallback
  }
}

// The panel's own address is left out on purpose: it is managed only under Settings.
export async function loadSources(): Promise<DomainSources> {
  const [sites, zones, redirects, mailDomains, domains] = await Promise.all([
    request<SiteData[] | null>('/sites'),
    optional(request<ZoneData>('/dns/zones'), { zones: [] }),
    request<RedirectData[] | null>('/redirects'),
    // Tokens limited to sites may not see e-mail (403); then the sidebar shows none.
    optional(request<MailDomainData[] | null>('/mail/domains'), null),
    // Domains added by hand; tokens limited to sites may not see them either.
    optional(request<AddedDomainData[] | null>('/domains'), null),
  ])
  return {
    sites: (sites ?? []).map((site) => ({
      name: site.name,
      domains: site.domains ?? [],
      live: site.current_release !== '',
      release: site.current_release || undefined,
    })),
    zones: (zones.zones ?? []).map((zone) => ({
      connectionId: zone.connection_id,
      connectionName: zone.connection_name,
      name: zone.name,
    })),
    redirects: (redirects ?? [])
      .filter((redirect) => redirect.source_path === '')
      .map((redirect) => ({ id: redirect.id, source: redirect.source_domain, target: redirect.target })),
    pathRedirectHosts: [
      ...new Set(
        (redirects ?? []).filter((redirect) => redirect.source_path !== '').map((redirect) => redirect.source_domain),
      ),
    ],
    mailDomains: mailDomains ? mailDomains.map((mail) => ({ domain: mail.domain, active: mail.active })) : undefined,
    domains: domains ? domains.map((domain) => domain.name) : undefined,
  }
}

// One lookup per site and per redirect; failed lookups leave the domain out, so its family stays "still checking".
export async function loadPointStates(sources: DomainSources): Promise<PointStates> {
  const states: PointStates = {}
  const record = (list: DomainStatus[]) => list.forEach((status) => (states[status.domain] = status.state))
  await Promise.all([
    ...sources.sites
      .filter((site) => site.domains.length > 0)
      .map((site) =>
        optional(request<DomainStatus[]>(`/sites/${encodeURIComponent(site.name)}/domains/status`), []).then(record),
      ),
    ...sources.redirects.map((redirect) =>
      optional(
        request<DomainStatus>(`/dns/domain-status?domain=${encodeURIComponent(redirect.source)}`).then((status) => [status]),
        [],
      ).then(record),
    ),
  ])
  return states
}

export type ZoneRecordsResult = { zones: ZoneRecords[]; failed: string[] }

// Reads which names exist in each connected DNS account holding the domain. A zone that can't be read is reported, not fatal.
export async function loadZoneRecords(zones: SourceZone[]): Promise<ZoneRecordsResult> {
  const results = await Promise.all(
    zones.map(async (zone) => {
      try {
        return { zone: zone.name, records: await listRecordNames(zone.connectionId, zone.name) }
      } catch {
        return { zone: zone.name, records: null }
      }
    }),
  )
  return {
    zones: results.flatMap((result) => (result.records ? [{ zone: result.zone, records: result.records }] : [])),
    failed: results.filter((result) => !result.records).map((result) => result.zone),
  }
}
