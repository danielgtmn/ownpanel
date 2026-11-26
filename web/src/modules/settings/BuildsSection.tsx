import { Button } from '@ui-sdk/react/button'
import { errorMessage } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { LoadingState, Section, StatusBadge } from '../../shared/ui/index.ts'
import { activityText, buildSummary, getBuildAvailability, type BuildAvailability } from './builds.ts'

const TITLE = 'Building sites'
const DESCRIPTION = 'Whether this server can build sites from their source itself.'

export function BuildsStatus({ info }: { info: BuildAvailability }) {
  const summary = buildSummary(info)
  const activity = activityText(info)
  return (
    <div className="build-status">
      <StatusBadge tone={summary.tone}>{summary.title}</StatusBadge>
      <p className={info.available ? undefined : 'muted'}>{summary.text}</p>
      {activity ? <p className="muted">Right now: {activity}.</p> : null}
    </div>
  )
}

export function BuildsSection() {
  const status = useResource(() => getBuildAvailability(), 'build status')
  return (
    <Section title={TITLE} description={DESCRIPTION}>
      {status.status === 'loading' ? <LoadingState label="Checking…" /> : null}
      {status.status === 'error' ? (
        <div className="notice update-check-failed">
          <span>{errorMessage(status.error)}</span>
          <Button onClick={status.retry}>Try again</Button>
        </div>
      ) : null}
      {status.status === 'ready' ? <BuildsStatus info={status.data} /> : null}
    </Section>
  )
}
