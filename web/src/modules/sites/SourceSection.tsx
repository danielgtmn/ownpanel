import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDate, formatDateTime, isoTimestamp } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { ErrorState, ExternalIcon, LoadingState, Section, StatusBadge } from '../../shared/ui/index.ts'
import { GitSourceDetails, NotifySecretDialog, type ShownSecret } from './GitSourceDetails.tsx'
import {
  getSiteSource,
  removeSiteSource,
  runAuthor,
  runStatus,
  sourceProblem,
  type Run,
  type SiteSource,
  type SiteSourceState,
} from './source.ts'
import { SourceDialog } from './SourceDialog.tsx'

const description = "Where this site's code comes from."

type RowProps = {
  site: string
  source: SiteSource
  publishingAvailable: boolean
  onChange: () => Promise<void>
  onSecret: (secret: ShownSecret) => void
}

function SourceRow({ site, source, publishingAvailable, onChange, onSecret }: RowProps) {
  const problem = sourceProblem(source)
  return (
    <ul className="record-list" aria-label="Source">
      <li className="record-row">
        <div className="record-main">
          <h3 className="record-title">
            {source.url ? (
              <a className="inline-link" href={source.url} target="_blank" rel="noreferrer noopener">
                {source.repository}
                <ExternalIcon />
              </a>
            ) : (
              source.repository
            )}
          </h3>
          <ul className="record-facts">
            {source.kind === 'git' && source.server ? <li>On {source.server}</li> : null}
            <li>
              Branch <span className="source-branch">{source.branch}</span>
            </li>
            <li>{source.publish_on_push ? 'Publish on push: on' : 'Publish on push: off'}</li>
            <li>
              Linked <time dateTime={isoTimestamp(source.linked_at)}>{formatDate(source.linked_at)}</time>
              {source.linked_by ? ` by ${source.linked_by}` : ''}
            </li>
          </ul>
          {problem ? (
            <p className="source-problem" role="status">
              {problem}
            </p>
          ) : null}
          {source.kind === 'git' ? <GitSourceDetails site={site} source={source} onChange={onChange} onSecret={onSecret} /> : null}
          {source.publish_on_push && !publishingAvailable ? (
            <p className="notice">
              Publishing on push starts once building on the server is available. Until then, publish files as before.
            </p>
          ) : null}
        </div>
      </li>
    </ul>
  )
}

/** The newest push. Message and author come from the repository and are shown as text. */
function LastRun({ run }: { run: Run }) {
  const status = runStatus(run)
  const author = runAuthor(run)
  return (
    <div className="source-run" aria-label="Latest push" role="group">
      <div className="source-run-head">
        <h4 className="source-run-title">Latest push</h4>
        <StatusBadge tone={status.tone}>{status.label}</StatusBadge>
      </div>
      <p className="source-run-message">
        <code className="source-run-commit" title={run.commit}>
          {run.short_commit}
        </code>{' '}
        {run.message || <span className="muted">No message</span>}
      </p>
      <p className="source-run-meta">
        {author ? `${author} · ` : ''}
        <time dateTime={isoTimestamp(run.created_at)}>{formatDateTime(run.created_at)}</time>
        {run.branch ? ` · ${run.branch}` : ''}
      </p>
      {run.status === 'queued' && run.waiting ? (
        <p className="source-run-note">Waits until building on the server is available.</p>
      ) : null}
      {run.detail && run.status !== 'skipped' ? <p className="source-run-note">{run.detail}</p> : null}
    </div>
  )
}

export function SourceSection({ site }: { site: string }) {
  const { isAdmin } = useSession()
  const state = useResource(() => getSiteSource(site), `source:${site}`)
  const [editing, setEditing] = useState(false)
  // A notification secret just made: shown once, dropped when its dialog closes.
  const [secret, setSecret] = useState<ShownSecret | null>(null)

  async function linked(next: SiteSourceState) {
    setEditing(false)
    if (next.source) toast.success(`${site} is linked to ${next.source.repository}, branch ${next.source.branch}.`)
    if (next.source && next.notify_secret) {
      setSecret({ secret: next.notify_secret, url: next.source.notify_url ?? '', server: next.source.server })
    }
    await state.reload()
  }

  async function unlink(source: SiteSource) {
    const yes = await confirm({
      title: `Unlink ${site} from ${source.repository}?`,
      description: 'The site and its published files stay as they are. Pushes to the repository no longer concern it.',
      confirmLabel: 'Unlink',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await removeSiteSource(site)
      toast.success(`${site} is no longer linked to ${source.repository}.`)
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await state.reload()
  }

  if (state.status === 'loading') {
    return (
      <Section title="Source" description={description}>
        <LoadingState label="Loading the source…" />
      </Section>
    )
  }
  if (state.status === 'error') {
    return (
      <Section title="Source" description={description}>
        <ErrorState title="Couldn't load the source" message={errorMessage(state.error)} onRetry={state.retry} />
      </Section>
    )
  }
  const { source, publishing_available: publishingAvailable, last_run: lastRun } = state.data
  const actions = (
    <>
      <Button data-variant={source ? undefined : 'solid'} onClick={() => setEditing(true)}>
        {source ? (
          <>
            Change<span className="visually-hidden"> the source</span>
          </>
        ) : (
          'Link repository'
        )}
      </Button>
      {source ? (
        <Button pendingAnnouncement="Unlinking" onClick={() => unlink(source)}>
          Unlink<span className="visually-hidden"> the repository</span>
        </Button>
      ) : null}
    </>
  )

  return (
    <Section title="Source" description={description} actions={actions}>
      {source ? (
        <SourceRow
          site={site}
          source={source}
          publishingAvailable={publishingAvailable}
          onChange={state.reload}
          onSecret={setSecret}
        />
      ) : (
        <p className="muted">
          Not linked to a repository. Link one to choose the branch this site is published from, once building on the
          server is available.
        </p>
      )}
      {lastRun ? <LastRun run={lastRun} /> : null}
      <SourceDialog
        open={editing}
        onOpenChange={setEditing}
        site={site}
        current={source}
        publishingAvailable={publishingAvailable}
        onLinked={linked}
        canUseGitHub={isAdmin}
      />
      <NotifySecretDialog shown={secret} onClose={() => setSecret(null)} />
    </Section>
  )
}
