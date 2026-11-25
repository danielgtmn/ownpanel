import {
  attentionText,
  familyStatus,
  familySummary,
  findFamily,
  STATUS_TEXT,
  type DomainSources,
  type Family,
  type PointStates,
} from '../../shared/families/index.ts'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { errorMessage } from '../../shared/api/index.ts'
import { adoptZone, listAvailableZones, listConnections, ownConnections, zonesNamed, type Zone } from '../../shared/dns/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { addDomainHref, connectAccountHref, hrefFor, navigate, newMailHref, newSiteHref } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { EmptyState, ErrorState, LoadingState, PageHeader, Section, StatusBadge } from '../../shared/ui/index.ts'
import { AddressesSection } from './AddressesSection.tsx'
import type { DomainData } from './useDomainData.ts'

// Offers to manage the domain's DNS here: from an account that has it, or by creating it at the provider.
function DnsOffer({ domain }: { domain: string }) {
  const { user } = useSession()
  const offer = useResource(async () => {
    const [connections, available] = await Promise.all([listConnections(), listAvailableZones()])
    return { accounts: ownConnections(connections, user), matches: zonesNamed(available.zones, domain) }
  }, `dns-offer:${domain}`)

  async function adopt(zone: Zone) {
    try {
      await adoptZone(zone.connection_id, zone.name)
      toast.success(`${zone.name} is now managed here.`)
      navigate('dns', String(zone.connection_id), zone.name)
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }

  if (offer.status === 'loading') return <LoadingState label="Looking for the domain in your accounts…" />
  if (offer.status === 'error') {
    return <ErrorState title="Couldn't look at your accounts" message={errorMessage(offer.error)} onRetry={offer.retry} />
  }
  const { accounts, matches } = offer.data
  if (matches.length > 0) {
    return (
      <div className="offer">
        <p className="muted">
          {matches.length === 1
            ? `Your account ${matches[0].connection_name} has ${domain}, but it isn't managed here yet.`
            : `Several of your accounts have ${domain}, but it isn't managed here yet.`}
        </p>
        <div className="offer-actions">
          {matches.map((zone) => (
            <Button key={zone.connection_id} pendingAnnouncement="Adding" onClick={() => adopt(zone)}>
              Manage DNS here
              {matches.length > 1 ? ` from ${zone.connection_name}` : null}
            </Button>
          ))}
        </div>
      </div>
    )
  }
  if (accounts.length > 0) {
    return (
      <p className="muted">
        None of your connected accounts has {domain}, so its records are changed wherever you manage the domain.{' '}
        <a href={addDomainHref(domain)}>Create it in your account</a>
      </p>
    )
  }
  return (
    <p className="muted">
      Its records are changed wherever you manage the domain. To manage them here, connect the account that has it.{' '}
      <a href={connectAccountHref()}>Connect an account</a>
    </p>
  )
}

function FamilyContent({ family, sources, states }: { family: Family; sources: DomainSources; states: PointStates }) {
  const status = familyStatus(family, states)
  const tone = status === 'ok' ? 'positive' : status === 'attention' ? 'warning' : 'neutral'
  return (
    <>
      <PageHeader
        title={family.domain}
        description={familySummary(family)}
        meta={
          <>
            <StatusBadge tone={tone}>{STATUS_TEXT[status].charAt(0).toUpperCase() + STATUS_TEXT[status].slice(1)}</StatusBadge>
            {status === 'attention' ? <span className="muted">{attentionText(family, states)}</span> : null}
          </>
        }
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
          <DnsOffer domain={family.domain} />
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
        description={`None of your sites, redirects or mail domains uses ${domain}, and it isn't managed here.`}
        action={<a href={allHref}>Show all domains</a>}
      />
    )
  }
  return <FamilyContent family={family} sources={data.sources} states={data.states} />
}
