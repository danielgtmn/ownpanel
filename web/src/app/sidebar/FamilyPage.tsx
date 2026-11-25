import {
  familyStatus,
  familySummary,
  findFamily,
  STATUS_TEXT,
  type DomainSources,
  type Family,
  type PointStates,
} from '../../shared/families/index.ts'
import { usePageTitle } from '../../shared/hooks/index.ts'
import { hrefFor, newMailHref, newSiteHref } from '../../shared/routing/index.ts'
import { EmptyState, ErrorState, LoadingState, PageHeader, Section, StatusBadge } from '../../shared/ui/index.ts'
import { AddressesSection } from './AddressesSection.tsx'
import type { DomainData } from './useDomainData.ts'

function FamilyContent({ family, sources, states }: { family: Family; sources: DomainSources; states: PointStates }) {
  const status = familyStatus(family, states)
  const tone = status === 'ok' ? 'positive' : status === 'attention' ? 'warning' : 'neutral'
  return (
    <>
      <PageHeader
        title={family.domain}
        description={familySummary(family)}
        meta={<StatusBadge tone={tone}>{STATUS_TEXT[status].charAt(0).toUpperCase() + STATUS_TEXT[status].slice(1)}</StatusBadge>}
      />
      <AddressesSection family={family} sources={sources} />
      <Section title="Websites" description={`Sites on ${family.domain} and its subdomains.`}>
        {family.sites.length === 0 ? (
          <p className="muted">
            No site uses this domain yet. <a href={newSiteHref(family.domain)}>Create a site</a>
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
                <p className="muted">
                  {site.otherFamilies.length > 0
                    ? `Here on ${site.domains.join(', ')}, and also on ${site.otherFamilies.join(', ')}.`
                    : site.domains.length > 0
                      ? `On ${site.domains.join(', ')}.`
                      : 'No address yet.'}
                </p>
              </li>
            ))}
          </ul>
        )}
      </Section>
      <Section title="E-mail" description={`Mailboxes and forwarding for addresses on ${family.domain} and its subdomains.`}>
        {family.mailDomains.length === 0 ? (
          <p className="muted">
            E-mail is not turned on for this domain. <a href={newMailHref(family.domain)}>Turn on e-mail</a>
          </p>
        ) : (
          <ul className="record-list" aria-label={`E-mail on ${family.domain}`}>
            {family.mailDomains.map((mail) => (
              <li className="record-row family-row" key={mail.domain}>
                <div className="family-row-head">
                  <a className="family-row-name" href={hrefFor('mail', mail.domain)}>
                    E-mail for {mail.domain}
                  </a>
                  <StatusBadge tone={mail.active ? 'positive' : 'warning'}>
                    {mail.active ? 'Receives mail' : 'Waiting for its mail records'}
                  </StatusBadge>
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
        description={`None of your sites, redirects, mail domains or DNS connections uses ${domain}.`}
        action={<a href={allHref}>Show all domains</a>}
      />
    )
  }
  return <FamilyContent family={family} sources={data.sources} states={data.states} />
}
