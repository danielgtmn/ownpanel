import { request, type DomainStatus } from '../../shared/api/index.ts'
import type { DomainSources, PointStates } from '../../shared/families/index.ts'

type SiteData = { name: string; domains: string[] | null; current_release: string }
type ZoneData = { zones?: { connection_id: number; connection_name: string; name: string }[] | null }
type RedirectData = { id: number; source_domain: string; source_path: string; target: string }

async function optional<T>(load: Promise<T>, fallback: T): Promise<T> {
  try {
    return await load
  } catch {
    return fallback
  }
}

// The panel's own address is left out on purpose: it is managed only under Settings.
export async function loadSources(): Promise<DomainSources> {
  const [sites, zones, redirects] = await Promise.all([
    request<SiteData[] | null>('/sites'),
    optional(request<ZoneData>('/dns/zones'), { zones: [] }),
    request<RedirectData[] | null>('/redirects'),
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
