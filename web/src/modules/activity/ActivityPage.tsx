import { Button } from '@ui-sdk/react/button'
import { useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDateTime, isoTimestamp } from '../../shared/format/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { EmptyState, ErrorState, LoadingState, PageHeader } from '../../shared/ui/index.ts'
import { listActivity } from './api.ts'
import { describeActivity, describeDetail, LIMITS, nextLimit } from './sentences.ts'
import type { Activity } from './types.ts'

function ActivityRow({ entry }: { entry: Activity }) {
  return (
    <li className="record-row activity-row">
      <div className="record-main">
        <p className="activity-sentence">{describeActivity(entry)}</p>
        {describeDetail(entry) ? <p className="muted activity-detail">{describeDetail(entry)}</p> : null}
      </div>
      <time className="muted activity-time" dateTime={isoTimestamp(entry.at)}>
        {formatDateTime(entry.at)}
      </time>
    </li>
  )
}

export function ActivityPage() {
  const [limit, setLimit] = useState<number>(LIMITS[0])
  const entries = useResource(() => listActivity(limit), `activity:${limit}`)
  const [previous, setPrevious] = useState<Activity[] | null>(null)
  usePageTitle('Activity')

  if (entries.status === 'ready' && entries.data !== previous) setPrevious(entries.data)
  const shown = entries.status === 'ready' ? entries.data : previous
  const more = nextLimit(limit)

  return (
    <>
      <PageHeader title="Activity" description="What people did in this panel, newest first." />
      {entries.status === 'loading' && !shown ? <LoadingState label="Loading activity…" /> : null}
      {entries.status === 'error' ? (
        <ErrorState title="Couldn't load the activity" message={errorMessage(entries.error)} onRetry={entries.retry} />
      ) : null}
      {entries.status !== 'error' && shown && shown.length === 0 ? (
        <EmptyState title="Nothing yet" description="Changes people make in the panel show up here." />
      ) : null}
      {entries.status !== 'error' && shown && shown.length > 0 ? (
        <>
          <ul className="record-list" aria-label="Activity">
            {shown.map((entry) => (
              <ActivityRow key={entry.id} entry={entry} />
            ))}
          </ul>
          <div className="activity-more">
            {more !== null && shown.length >= limit ? (
              <Button disabled={entries.status === 'loading'} onClick={() => setLimit(more)}>
                {entries.status === 'loading' ? 'Loading…' : 'Show more'}
              </Button>
            ) : null}
            {more === null && shown.length >= limit ? (
              <p className="muted">Showing the latest {limit} entries.</p>
            ) : null}
          </div>
        </>
      ) : null}
    </>
  )
}
