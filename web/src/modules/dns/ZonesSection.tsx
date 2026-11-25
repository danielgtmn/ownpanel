import { errorMessage } from '../../shared/api/index.ts'
import type { Resource } from '../../shared/hooks/index.ts'
import { hrefFor } from '../../shared/routing/index.ts'
import { Button } from '@ui-sdk/react/button'
import { connectAccountHref } from '../../shared/routing/index.ts'
import { EmptyState, ErrorState, LoadingState, Section } from '../../shared/ui/index.ts'
import type { ZoneList } from './types.ts'
import { describeZones } from './zones.ts'

type Props = { zones: Resource<ZoneList>; hasAccounts: boolean; onAdd: () => void }

export function ZonesSection({ zones, hasAccounts, onAdd }: Props) {
  const view = zones.status === 'ready' ? describeZones(zones.data) : null
  return (
    <Section
      title="Domains"
      description="The domains you manage here, and those your sites, redirects or e-mail use. Pick one to see and change its DNS records."
    >
      {zones.status === 'loading' ? <LoadingState label="Loading your domains…" /> : null}
      {zones.status === 'error' ? (
        <ErrorState title="Couldn't load your domains" message={errorMessage(zones.error)} onRetry={zones.retry} />
      ) : null}
      {view
        ? view.notices.map((notice) => (
            <p className="notice notice-problem" role="status" key={notice.key}>
              {notice.text}
            </p>
          ))
        : null}
      {view && view.rows.length === 0 && view.notices.length === 0 ? (
        <EmptyState
          inSection
          title="No domains yet"
          description={
            hasAccounts
              ? 'Add a domain from your connected accounts, or create a new one.'
              : 'Connect the account at the company that manages your domains first.'
          }
          action={
            hasAccounts ? (
              <Button onClick={onAdd}>Add domain</Button>
            ) : (
              <Button onClick={() => (window.location.hash = connectAccountHref())}>Connect an account</Button>
            )
          }
        />
      ) : null}
      {view && view.rows.length > 0 ? (
        <ul className="record-list" aria-label="Your domains">
          {view.rows.map((row) => (
            <li className="record-row" key={row.key}>
              <div className="record-main">
                <h3 className="record-title">
                  <a href={hrefFor('dns', String(row.connectionId), row.name)}>{row.name}</a>
                </h3>
                <ul className="record-facts">
                  <li>{row.connectionName}</li>
                  {row.readOnly ? <li>Read only</li> : null}
                  {row.usedBy.length > 0 ? <li>In use</li> : null}
                </ul>
              </div>
            </li>
          ))}
        </ul>
      ) : null}
    </Section>
  )
}
