import type { Family, FamilySite } from '../../shared/families/index.ts'
import { copyText } from '../../shared/ui/index.ts'

// The site whose icon stands for a domain: the first live one serving the domain itself or its
// www address, else the first live one.
export function mainSite(family: Family): FamilySite | null {
  const live = family.sites.filter((site) => site.live)
  const own = [family.domain, `www.${family.domain}`]
  return live.find((site) => site.domains.some((domain) => own.includes(domain))) ?? live[0] ?? null
}

// The release id makes the browser fetch the icon again after publishing.
export function iconUrl(site: FamilySite): string {
  const base = `/api/sites/${encodeURIComponent(site.name)}/icon`
  return site.release ? `${base}?v=${encodeURIComponent(site.release)}` : base
}

export function siteAddress(family: Family): string | null {
  const domain = mainSite(family)?.domains[0]
  return domain ? `https://${domain}/` : null
}

export function openSite(family: Family): boolean {
  const address = siteAddress(family)
  if (!address) return false
  window.open(address, '_blank', 'noopener,noreferrer')
  return true
}

export function copyDomain(family: Family): Promise<void> {
  return copyText(family.domain)
}
