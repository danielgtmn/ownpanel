import { errorMessage } from '../../shared/api/index.ts'
import type { Resource } from '../../shared/hooks/index.ts'
import { hrefFor } from '../../shared/routing/index.ts'
import { EmptyState, ErrorState, LoadingState, Section } from '../../shared/ui/index.ts'
import type { ZoneList } from './types.ts'
import { describeZones } from './zones.ts'

export function ZonesSection({ zones, hasConnections }: { zones: Resource<ZoneList>; hasConnections: boolean }) {
  const view = zones.status === 'ready' ? describeZones(zones.data) : null
  return (
    <Section title="Domains" description="Pick a domain to see and change its DNS records.">
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
          title="No domains yet"
          description={
            hasConnections
              ? 'The connected accounts don\'t have any domains yet.'
              : 'Connect Hetzner above to see your domains here.'
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
                </ul>
              </div>
            </li>
          ))}
        </ul>
      ) : null}
    </Section>
  )
}
