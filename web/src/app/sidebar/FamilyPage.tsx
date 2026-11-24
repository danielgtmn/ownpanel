import type { DomainState } from '../../shared/api/index.ts'
import {
  familyStatus,
  familySummary,
  findFamily,
  STATUS_TEXT,
  type Family,
  type PointStates,
} from '../../shared/families/index.ts'
import { usePageTitle } from '../../shared/hooks/index.ts'
import { hrefFor, hrefWithQuery } from '../../shared/routing/index.ts'
import { EmptyState, ErrorState, LoadingState, PageHeader, Section, StatusBadge } from '../../shared/ui/index.ts'
import type { DomainData } from './useDomainData.ts'

const POINT_TEXT: Record<DomainState, string> = {
  points_here: 'Points to this server',
  points_elsewhere: 'Points somewhere else',
  no_record: 'No DNS record yet',
  lookup_failed: "Couldn't check",
}

function PointBadge({ state }: { state: DomainState | undefined }) {
  if (!state) return <StatusBadge tone="neutral">Checking…</StatusBadge>
  const tone = state === 'points_here' ? 'positive' : state === 'lookup_failed' ? 'neutral' : 'warning'
  return <StatusBadge tone={tone}>{POINT_TEXT[state]}</StatusBadge>
}

function DomainList({ domains, states, label }: { domains: string[]; states: PointStates; label: string }) {
  return (
    <ul className="family-domains" aria-label={label}>
      {domains.map((domain) => (
        <li key={domain}>
          <span className="family-domain-name">{domain}</span>
          <PointBadge state={states[domain]} />
        </li>
      ))}
    </ul>
  )
}

function FamilyContent({ family, states }: { family: Family; states: PointStates }) {
  const status = familyStatus(family, states)
  const tone = status === 'ok' ? 'positive' : status === 'attention' ? 'warning' : 'neutral'
  return (
    <>
      <PageHeader
        title={family.domain}
        description={familySummary(family)}
        meta={<StatusBadge tone={tone}>{STATUS_TEXT[status].charAt(0).toUpperCase() + STATUS_TEXT[status].slice(1)}</StatusBadge>}
      />
      <Section title="Websites" description={`Sites on ${family.domain} and its subdomains.`}>
        {family.sites.length === 0 ? (
          <p className="muted">
            No site uses this domain yet. <a href={hrefWithQuery(['sites'], { new: '1' })}>Create a site</a>
          </p>
        ) : (
          <ul className="record-list" aria-label={`Sites on ${family.domain}`}>
            {family.sites.map((site) => (
              <li className="record-row family-row" key={site.name}>
                <div className="family-row-head">
                  <a className="family-row-name" href={hrefFor('sites', site.name)}>
                    {site.name}
                  </a>
                  <StatusBadge tone={site.live ? 'positive' : 'warning'}>{site.live ? 'Live' : 'No content yet'}</StatusBadge>
                </div>
                {site.otherFamilies.length > 0 ? (
                  <p className="muted">The same site also answers on {site.otherFamilies.join(', ')}.</p>
                ) : null}
                <DomainList domains={site.domains} states={states} label={`Domains of ${site.name}`} />
              </li>
            ))}
          </ul>
        )}
      </Section>
      <Section
        title="Redirects"
        description={`Domains under ${family.domain} that send visitors elsewhere.`}
        actions={<a href={hrefWithQuery(['redirects'], { family: family.domain })}>Show on the Redirects page</a>}
      >
        {family.redirects.length === 0 ? (
          <p className="muted">None of these domains redirects.</p>
        ) : (
          <ul className="record-list" aria-label={`Redirects on ${family.domain}`}>
            {family.redirects.map((redirect) => (
              <li className="record-row family-row" key={redirect.id}>
                <div className="family-row-head">
                  <span className="family-row-name">
                    {redirect.source} → {redirect.target}
                  </span>
                  <PointBadge state={states[redirect.source]} />
                </div>
              </li>
            ))}
          </ul>
        )}
      </Section>
      <Section title="DNS" description="Where the records of this domain are managed.">
        {family.zones.length === 0 ? (
          <p className="muted">
            None of your DNS connections holds {family.domain}, so its records are changed at your domain provider.{' '}
            <a href={hrefFor('dns')}>Connect a DNS account</a>
          </p>
        ) : (
          <ul className="record-list" aria-label={`DNS of ${family.domain}`}>
            {family.zones.map((zone) => (
              <li className="record-row family-row" key={`${zone.connectionId}/${zone.name}`}>
                <div className="family-row-head">
                  <a className="family-row-name" href={hrefFor('dns', String(zone.connectionId), zone.name)}>
                    DNS records of {zone.name}
                  </a>
                  <span className="muted">{zone.connectionName}</span>
                </div>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </>
  )
}

export function FamilyPage({ domain, data, allHref }: { domain: string; data: DomainData; allHref: string }) {
  const family = data.status === 'ready' ? findFamily(data.families, domain) : undefined
  usePageTitle(domain)
  if (data.status === 'loading') return <LoadingState label="Loading your domains…" />
  if (data.status === 'error') {
    return (
      <ErrorState
        title="Couldn't load your domains"
        message="Check your connection and try again."
        onRetry={data.retry}
      />
    )
  }
  if (!family) {
    return (
      <EmptyState
        title="Domain not found"
        description={`None of your sites, redirects or DNS connections uses ${domain}.`}
        action={<a href={allHref}>Show all domains</a>}
      />
    )
  }
  return <FamilyContent family={family} states={data.states} />
}
