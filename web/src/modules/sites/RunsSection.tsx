import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { useEffect, useState, type ReactNode } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDateTime, isoTimestamp } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { ErrorState, ExternalIcon, FormDialog, LoadingState, Section, StatusBadge } from '../../shared/ui/index.ts'
import { commitLink, getSiteRuns, runDuration, runNote, RUNS_REFRESH_MS, runTrigger } from './runs.ts'
import { runAuthor, runStatus, type Run } from './source.ts'

const title = 'Builds'
const description = 'Every push to the linked branch starts a build that publishes the site. The newest come first.'
const descriptionWaiting =
  'Every push to the linked branch is listed here. Once building on the server is available, each one is built and published.'

/** The commit's short id, a link to its page on GitHub when known. */
export function CommitId({ run }: { run: Run }) {
  const href = commitLink(run)
  const id = (
    <code className="run-commit" title={run.commit}>
      {run.short_commit}
    </code>
  )
  if (!href) return id
  return (
    <a className="inline-link" href={href} target="_blank" rel="noreferrer noopener">
      {id}
      <span className="visually-hidden"> (open the commit on GitHub)</span>
      <ExternalIcon />
    </a>
  )
}

/** Time of a run as a <time> element. */
function When({ value }: { value: string }) {
  return <time dateTime={isoTimestamp(value)}>{formatDateTime(value)}</time>
}

type RowProps = { run: Run; onOpen: (run: Run) => void }

/** One run. Message, author and pusher come from the repository: React shows them as text. */
export function RunRow({ run, onOpen }: RowProps) {
  const status = runStatus(run)
  const author = runAuthor(run)
  const note = runNote(run)
  return (
    <li className="record-row run-row">
      <div className="record-main">
        <p className="run-message">{run.message || <span className="muted">No message</span>}</p>
        <ul className="record-facts">
          <li>
            <CommitId run={run} />
          </li>
          {author ? <li>{author}</li> : null}
          <li>{runTrigger(run)}</li>
          <li>
            <When value={run.created_at} />
          </li>
        </ul>
        {note ? <p className="run-note">{note}</p> : null}
      </div>
      <div className="run-side">
        <StatusBadge tone={status.tone}>{status.label}</StatusBadge>
        <Button onClick={() => onOpen(run)}>
          Details<span className="visually-hidden"> of the build of {run.short_commit}</span>
        </Button>
      </div>
    </li>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="run-fact">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  )
}

/**
 * The output of the build. Builds on the server and their logs come later
 * (#113, #115): this is where the log goes.
 */
function RunLog({ run, publishingAvailable }: { run: Run; publishingAvailable: boolean }) {
  let text: string
  if (run.status === 'skipped') text = 'This push was not built: a newer one replaced it before it started.'
  else if (!publishingAvailable) text = 'The output of the build appears here once building on the server is available.'
  else text = 'There is no output for this build.'
  return (
    <section className="run-log" aria-label="Output of the build">
      <h3 className="run-log-title">Output</h3>
      <p className="muted">{text}</p>
    </section>
  )
}

export function RunDetails({ run, publishingAvailable }: { run: Run; publishingAvailable: boolean }) {
  const status = runStatus(run)
  const note = runNote(run)
  const author = runAuthor(run)
  const duration = runDuration(run)
  return (
    <div className="run-details">
      <div className="run-details-status">
        <StatusBadge tone={status.tone}>{status.label}</StatusBadge>
        {note ? <p className="run-note">{note}</p> : null}
      </div>
      <dl className="run-facts">
        <Fact label="Commit">
          <CommitId run={run} />
        </Fact>
        <Fact label="Message">{run.message || <span className="muted">No message</span>}</Fact>
        {author ? <Fact label="Author">{author}</Fact> : null}
        {run.pusher && run.pusher !== run.author_login ? <Fact label="Pushed by">{run.pusher}</Fact> : null}
        <Fact label="Repository">{run.repository}</Fact>
        {run.branch ? (
          <Fact label="Branch">
            <span className="source-branch">{run.branch}</span>
          </Fact>
        ) : null}
        <Fact label="Started by">{runTrigger(run)}</Fact>
        <Fact label="Received">
          <When value={run.created_at} />
        </Fact>
        {run.started_at ? (
          <Fact label="Build started">
            <When value={run.started_at} />
          </Fact>
        ) : null}
        {run.finished_at ? (
          <Fact label="Finished">
            <When value={run.finished_at} />
            {duration ? ` (took ${duration})` : ''}
          </Fact>
        ) : null}
      </dl>
      <RunLog run={run} publishingAvailable={publishingAvailable} />
    </div>
  )
}

function RunDialog({
  run,
  publishingAvailable,
  onClose,
}: {
  run: Run | null
  publishingAvailable: boolean
  onClose: () => void
}) {
  // Keep the last run while the dialog closes, so it doesn't empty out.
  const [shown, setShown] = useState(run)
  if (run && run !== shown) setShown(run)
  return (
    <FormDialog
      open={run !== null}
      onOpenChange={(open) => (open ? null : onClose())}
      title={shown ? `Build of ${shown.short_commit}` : 'Build'}
      description={shown ? `${runTrigger(shown)} to ${shown.branch || shown.ref} of ${shown.repository}` : ''}
    >
      {shown ? <RunDetails run={shown} publishingAvailable={publishingAvailable} /> : null}
      <div className="form-actions">
        <DialogClose>Close</DialogClose>
      </div>
    </FormDialog>
  )
}

/** Looks for new pushes now and then while the page is visible. */
function useRefresh(reload: () => Promise<void>, active: boolean) {
  useEffect(() => {
    if (!active) return
    const tick = () => {
      if (document.visibilityState === 'visible') void reload()
    }
    const timer = window.setInterval(tick, RUNS_REFRESH_MS)
    document.addEventListener('visibilitychange', tick)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [reload, active])
}

export function RunsSection({ site }: { site: string }) {
  const runs = useResource(() => getSiteRuns(site), `runs:${site}`)
  const [openId, setOpenId] = useState<string | null>(null)
  useRefresh(runs.reload, runs.status === 'ready')

  if (runs.status === 'loading') {
    return (
      <Section title={title} description={descriptionWaiting}>
        <LoadingState label="Loading the builds…" />
      </Section>
    )
  }
  if (runs.status === 'error') {
    return (
      <Section title={title} description={descriptionWaiting}>
        <ErrorState title="Couldn't load the builds" message={errorMessage(runs.error)} onRetry={runs.retry} />
      </Section>
    )
  }
  const { runs: list, kept, publishing_available: publishingAvailable } = runs.data
  // The open run follows the list, so its status stays current.
  const open = openId ? (list.find((run) => run.id === openId) ?? null) : null

  return (
    <Section
      title={title}
      description={`${publishingAvailable ? description : descriptionWaiting} The last ${kept} are kept.`}
    >
      {list.length === 0 ? (
        <p className="muted">
          No pushes yet. Once the site is linked to a repository with publishing on push turned on, every push to its
          branch appears here.
        </p>
      ) : (
        <ol className="record-list run-list" aria-label={`Builds of ${site}`}>
          {list.map((run) => (
            <RunRow key={run.id} run={run} onOpen={(next) => setOpenId(next.id)} />
          ))}
        </ol>
      )}
      <RunDialog run={open} publishingAvailable={publishingAvailable} onClose={() => setOpenId(null)} />
    </Section>
  )
}
