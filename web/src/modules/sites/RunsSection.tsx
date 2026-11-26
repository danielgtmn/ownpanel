import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { toast } from '@ui-sdk/react/toast'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDateTime, isoTimestamp } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { clearQuery, useQuery } from '../../shared/routing/index.ts'
import { ErrorState, ExternalIcon, FormDialog, LoadingState, Section, StatusBadge } from '../../shared/ui/index.ts'
import {
  ACTIVE_REFRESH_MS,
  cancelRun,
  getBuildSettings,
  getRunLog,
  LOG_REFRESH_MS,
  runFromQuery,
  settingsSummary,
  startBuild,
  type BuildSettings,
} from './builds.ts'
import { BuildSettingsDialog } from './BuildSettingsDialog.tsx'
import { commitLink, getSiteRuns, runActive, runDuration, runNote, RUNS_REFRESH_MS, runTrigger } from './runs.ts'
import { runAuthor, runStatus, type Run } from './source.ts'

const title = 'Builds'
const description =
  'Every push to the linked branch, and every “Build now”, builds the site and publishes it. A failed build leaves the site as it is. The newest come first.'
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

type RowProps = { run: Run; onOpen: (run: Run) => void; onCancel?: (run: Run) => void }

/** One run. Message, author and pusher come from the repository: React shows them as text. */
export function RunRow({ run, onOpen, onCancel }: RowProps) {
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
        {onCancel && runActive(run) ? (
          <Button pendingAnnouncement="Cancelling" onClick={() => onCancel(run)}>
            Cancel<span className="visually-hidden"> the build of {run.short_commit}</span>
          </Button>
        ) : null}
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

type LogState = { text: string; next: number; done: boolean; truncated: boolean; error: string | null }

/** Reads a run's log from where it stopped, again and again while the build runs. */
function useRunLog(site: string, run: Run, enabled: boolean): LogState {
  const [state, setState] = useState<LogState>({ text: '', next: 0, done: false, truncated: false, error: null })
  const active = runActive(run)
  const status = run.status
  useEffect(() => {
    if (!enabled) return
    let stopped = false
    let timer: number | undefined
    let next = 0
    setState({ text: '', next: 0, done: false, truncated: false, error: null })
    const read = async () => {
      try {
        const part = await getRunLog(site, run.id, next)
        if (stopped) return
        next = part.next
        setState((s) => ({ text: s.text + part.text, next, done: part.done, truncated: part.truncated, error: null }))
        if (part.text !== '' && !part.done) {
          void read()
          return
        }
        if (!part.done) timer = window.setTimeout(read, LOG_REFRESH_MS)
      } catch (error) {
        if (!stopped) setState((s) => ({ ...s, error: errorMessage(error) }))
      }
    }
    void read()
    return () => {
      stopped = true
      window.clearTimeout(timer)
    }
    // The status is a dependency so a log that was done waiting is read again once the run ends.
  }, [site, run.id, enabled, active, status])
  return state
}

function RunLog({ site, run, publishingAvailable }: { site: string; run: Run; publishingAvailable: boolean }) {
  const enabled = publishingAvailable && run.status !== 'skipped' && run.status !== 'queued'
  const log = useRunLog(site, run, enabled)
  const box = useRef<HTMLPreElement>(null)
  const follow = useRef(true)
  useEffect(() => {
    const el = box.current
    if (el && follow.current) el.scrollTop = el.scrollHeight
  }, [log.text])

  let note: string | null = null
  if (run.status === 'skipped') note = 'This push was not built: a newer one replaced it before it started.'
  else if (!publishingAvailable) note = 'The output of the build appears here once building on the server is available.'
  else if (run.status === 'queued') note = 'The output appears here once the build starts.'
  else if (log.error) note = `Couldn't load the output: ${log.error}`
  else if (log.done && log.text === '') note = 'There is no output for this build.'
  else if (log.text === '') note = runActive(run) ? 'Waiting for output…' : 'Loading the output…'

  return (
    <section className="run-log" aria-label="Output of the build">
      <h3 className="run-log-title">Output</h3>
      {log.text ? (
        <pre
          ref={box}
          className="run-log-text"
          tabIndex={0}
          aria-label="Output of the build"
          aria-live={runActive(run) ? 'polite' : undefined}
          onScroll={(event) => {
            const el = event.currentTarget
            follow.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
          }}
        >
          {log.text}
        </pre>
      ) : null}
      {log.truncated ? <p className="muted">The output was longer than the panel keeps: its middle is left out.</p> : null}
      {note ? <p className="muted">{note}</p> : null}
    </section>
  )
}

export function RunDetails({ site, run, publishingAvailable }: { site: string; run: Run; publishingAvailable: boolean }) {
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
        {run.release ? (
          <Fact label="Published as">
            version <code>{run.release}</code>
          </Fact>
        ) : null}
      </dl>
      <RunLog site={site} run={run} publishingAvailable={publishingAvailable} />
    </div>
  )
}

function RunDialog({
  site,
  run,
  publishingAvailable,
  onClose,
  onCancel,
}: {
  site: string
  run: Run | null
  publishingAvailable: boolean
  onClose: () => void
  onCancel: (run: Run) => void
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
      {shown ? <RunDetails site={site} run={shown} publishingAvailable={publishingAvailable} /> : null}
      <div className="form-actions">
        {shown && runActive(shown) ? (
          <Button pendingAnnouncement="Cancelling" onClick={() => onCancel(shown)}>
            Cancel the build
          </Button>
        ) : null}
        <DialogClose>Close</DialogClose>
      </div>
    </FormDialog>
  )
}

/** Looks for new pushes now and then while the page is visible, more often while a build waits or runs. */
function useRefresh(reload: () => Promise<void>, active: boolean, every: number) {
  useEffect(() => {
    if (!active) return
    const tick = () => {
      if (document.visibilityState === 'visible') void reload()
    }
    const timer = window.setInterval(tick, every)
    document.addEventListener('visibilitychange', tick)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [reload, active, every])
}

/** "Build now" and the build settings, above the list. */
function BuildControls({
  site,
  settings,
  onChanged,
  onStarted,
}: {
  site: string
  settings: BuildSettings
  onChanged: () => Promise<void>
  onStarted: (run: Run) => Promise<void>
}) {
  const [editing, setEditing] = useState(false)
  async function buildNow() {
    try {
      const run = await startBuild(site)
      toast.success(`Building ${site} from commit ${run.short_commit}.`)
      await onStarted(run)
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }
  return (
    <>
      <p className="build-summary">{settingsSummary(settings)}</p>
      <div className="build-controls">
        <Button data-variant={settings.configured ? undefined : 'solid'} onClick={() => setEditing(true)}>
          Build settings
        </Button>
        {settings.configured ? (
          <Button data-variant="solid" pendingAnnouncement="Starting the build" onClick={buildNow}>
            Build now
          </Button>
        ) : null}
      </div>
      <BuildSettingsDialog
        open={editing}
        onOpenChange={setEditing}
        site={site}
        settings={settings}
        onSaved={async () => {
          setEditing(false)
          toast.success(`The build settings of ${site} are saved.`)
          await onChanged()
        }}
      />
    </>
  )
}

export function RunsSection({ site }: { site: string }) {
  const runs = useResource(() => getSiteRuns(site), `runs:${site}`)
  const settings = useResource(() => getBuildSettings(site), `build-settings:${site}`)
  const query = useQuery()
  const [openId, setOpenId] = useState<string | null>(null)
  const anyActive = runs.status === 'ready' && runs.data.runs.some(runActive)
  useRefresh(runs.reload, runs.status === 'ready', anyActive ? ACTIVE_REFRESH_MS : RUNS_REFRESH_MS)
  // A link to a run (from GitHub) opens it once.
  const fromQuery = runFromQuery(query)
  const [linked, setLinked] = useState<string | null>(null)
  if (fromQuery && fromQuery !== linked) {
    setLinked(fromQuery)
    setOpenId(fromQuery)
  }
  useEffect(() => {
    if (fromQuery) clearQuery()
  }, [fromQuery])

  async function cancel(run: Run) {
    try {
      const next = await cancelRun(site, run.id)
      toast.success(next.status === 'cancelled' ? `The build of ${run.short_commit} is cancelled.` : 'Stopping the build…')
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await runs.reload()
  }

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
      {publishingAvailable && settings.status === 'ready' ? (
        <BuildControls
          site={site}
          settings={settings.data}
          onChanged={settings.reload}
          onStarted={async (run) => {
            await runs.reload()
            setOpenId(run.id)
          }}
        />
      ) : null}
      {publishingAvailable && settings.status === 'error' ? (
        <p className="form-error">Couldn't load the build settings: {errorMessage(settings.error)}</p>
      ) : null}
      {list.length === 0 ? (
        <p className="muted">
          No builds yet. Once the site is linked to a repository with publishing on push turned on, every push to its
          branch appears here, and so does every “Build now”.
        </p>
      ) : (
        <ol className="record-list run-list" aria-label={`Builds of ${site}`}>
          {list.map((run) => (
            <RunRow key={run.id} run={run} onOpen={(next) => setOpenId(next.id)} onCancel={cancel} />
          ))}
        </ol>
      )}
      <RunDialog
        site={site}
        run={open}
        publishingAvailable={publishingAvailable}
        onClose={() => setOpenId(null)}
        onCancel={cancel}
      />
    </Section>
  )
}
