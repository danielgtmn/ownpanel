import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDate, isoTimestamp } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { ErrorState, ExternalIcon, LoadingState, Section } from '../../shared/ui/index.ts'
import { PreviewsDialog } from './PreviewsDialog.tsx'
import {
  getSiteSource,
  previewsSummary,
  removeSiteSource,
  sourceProblem,
  type SiteSource,
  type SiteSourceState,
} from './source.ts'
import { SourceDialog } from './SourceDialog.tsx'

const description = "Where this site's code comes from."

function SourceRow({ source, publishingAvailable }: { source: SiteSource; publishingAvailable: boolean }) {
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
            <li>
              Branch <span className="source-branch">{source.branch}</span>
            </li>
            <li>{source.publish_on_push ? 'Publish on push: on' : 'Publish on push: off'}</li>
            {source.kind === 'github' ? <li>{previewsSummary(source)}</li> : null}
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

export function SourceSection({ site }: { site: string }) {
  const { isAdmin } = useSession()
  const state = useResource(() => getSiteSource(site), `source:${site}`)
  const [editing, setEditing] = useState(false)
  const [previews, setPreviews] = useState(false)

  async function previewsSaved() {
    setPreviews(false)
    toast.success(`Saved which pull requests are built as previews of ${site}.`)
    await state.reload()
  }

  async function linked(next: SiteSourceState) {
    setEditing(false)
    if (next.source) toast.success(`${site} is linked to ${next.source.repository}, branch ${next.source.branch}.`)
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
  const { source, publishing_available: publishingAvailable } = state.data
  const actions = (
    <>
      {isAdmin ? (
        <Button data-variant={source ? undefined : 'solid'} onClick={() => setEditing(true)}>
          {source ? (
            <>
              Change<span className="visually-hidden"> the source</span>
            </>
          ) : (
            'Link repository'
          )}
        </Button>
      ) : null}
      {source?.kind === 'github' ? (
        <Button onClick={() => setPreviews(true)}>
          Pull requests<span className="visually-hidden">: choose which get previews</span>
        </Button>
      ) : null}
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
        <SourceRow source={source} publishingAvailable={publishingAvailable} />
      ) : (
        <p className="muted">
          {isAdmin
            ? 'Not linked to a repository. Link one to choose the branch this site is published from, once building on the server is available.'
            : 'Not linked to a repository. An administrator can link it to one of the connected repositories.'}
        </p>
      )}
      {source?.kind === 'github' ? (
        <PreviewsDialog
          open={previews}
          onOpenChange={setPreviews}
          site={site}
          source={source}
          onSaved={previewsSaved}
        />
      ) : null}
      {isAdmin ? (
        <SourceDialog
          open={editing}
          onOpenChange={setEditing}
          site={site}
          current={source}
          publishingAvailable={publishingAvailable}
          onLinked={linked}
        />
      ) : null}
    </Section>
  )
}
