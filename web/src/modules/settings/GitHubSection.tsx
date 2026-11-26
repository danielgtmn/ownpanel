import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { toast } from '@ui-sdk/react/toast'
import { useEffect, useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { formatDate, isoTimestamp } from '../../shared/format/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { clearQuery, useQuery } from '../../shared/routing/index.ts'
import { EmptyState, ErrorState, ExternalIcon, FormDialog, LoadingState, Section } from '../../shared/ui/index.ts'
import { ConnectGitHubDialog } from './ConnectGitHubDialog.tsx'
import {
  accountLabel,
  finishChoosingRepositories,
  finishConnecting,
  getGitHub,
  readGitHubReturn,
  refreshGitHub,
  removeConnection,
  repositoriesLabel,
  startChoosingRepositories,
  type GitHubConnection,
  type GitHubRemoved,
  type GitHubReturn,
} from './github.ts'

async function chooseRepositories(connection: GitHubConnection) {
  try {
    const { url } = await startChoosingRepositories(connection.id)
    window.location.assign(url)
  } catch (error) {
    toast.error(errorMessage(error))
  }
}

type RowProps = { connection: GitHubConnection; onRemove: (connection: GitHubConnection) => Promise<void> }

export function ConnectionRow({ connection, onRemove }: RowProps) {
  const installed = connection.installations.length > 0
  return (
    <li className="record-row">
      <div className="record-main">
        <h3 className="record-title">{connection.owner}</h3>
        <ul className="record-facts">
          <li>{accountLabel(connection.owner_type)}</li>
          <li>
            Connected <time dateTime={isoTimestamp(connection.created_at)}>{formatDate(connection.created_at)}</time>
            {connection.created_by ? ` by ${connection.created_by}` : ''}
          </li>
          <li>
            <a className="inline-link" href={connection.html_url} target="_blank" rel="noreferrer noopener">
              App “{connection.name}”
              <ExternalIcon />
            </a>
          </li>
        </ul>
        {installed ? (
          <ul className="github-installations" aria-label={`Repositories of ${connection.owner}`}>
            {connection.installations.map((installation) => (
              <li key={installation.id}>
                <span>{repositoriesLabel(installation)}</span>
                {installation.account !== connection.owner ? <span className="muted"> of {installation.account}</span> : null}
                {installation.suspended ? (
                  <span className="connection-health" data-tone="problem">
                    {' '}
                    Paused on GitHub, so the panel can't use it.
                  </span>
                ) : null}
                {' · '}
                <a className="inline-link" href={installation.settings_url} target="_blank" rel="noreferrer noopener">
                  Change on GitHub
                  <ExternalIcon />
                </a>
              </li>
            ))}
          </ul>
        ) : (
          <p className="connection-health" data-tone="unknown">
            Not finished: choose which repositories the panel may see.
          </p>
        )}
        {connection.last_error.trim() ? (
          <p className="connection-health" data-tone="problem">
            {connection.last_error.trim()}
          </p>
        ) : null}
      </div>
      <div className="record-actions">
        {installed ? null : (
          <Button data-variant="solid" pendingAnnouncement="Opening GitHub" onClick={() => chooseRepositories(connection)}>
            Choose repositories<span className="visually-hidden"> of {connection.owner}</span>
          </Button>
        )}
        <Button data-variant="destructive" pendingAnnouncement="Removing" onClick={() => onRemove(connection)}>
          Remove<span className="visually-hidden"> {connection.owner}</span>
        </Button>
      </div>
    </li>
  )
}

function RemovedDialog({ removed, onClose }: { removed: GitHubRemoved | null; onClose: () => void }) {
  return (
    <FormDialog
      open={removed !== null}
      onOpenChange={(open) => (open ? null : onClose())}
      title="Delete the app on GitHub too"
      description="The panel no longer has access. The app it created still exists on GitHub until you delete it there, under “Advanced” on its settings page."
    >
      <div className="form-actions">
        <DialogClose>Close</DialogClose>
        {removed ? (
          <Button
            data-variant="solid"
            render={<a href={removed.settings_url} target="_blank" rel="noreferrer noopener" />}
          >
            Open the app on GitHub
            <ExternalIcon />
          </Button>
        ) : null}
      </div>
    </FormDialog>
  )
}

const sentence = (text: string) => (/[.!?]$/.test(text.trim()) ? text.trim() : `${text.trim()}.`)

// GitHub sends people back with codes that work once; StrictMode runs effects twice.
const handledReturns = new Set<string>()

async function finishReturn(step: GitHubReturn): Promise<GitHubConnection | null> {
  switch (step.kind) {
    case 'created': {
      const connection = await finishConnecting(step.code, step.state)
      toast.success(`GitHub is connected for ${connection.owner}. Now choose which repositories the panel may see.`, {
        action: { label: 'Choose repositories', onAction: () => void chooseRepositories(connection) },
      })
      return connection
    }
    case 'installed': {
      const connection = await finishChoosingRepositories(step.installationId, step.state)
      toast.success(`The panel can now see the chosen repositories of ${connection.owner}.`)
      return connection
    }
    case 'installed-elsewhere':
      await refreshGitHub()
      toast.success('Updated with the changes from GitHub.')
      return null
    case 'requested':
      toast.info('An owner of the organisation has to approve this on GitHub first. Come back afterwards and choose the repositories.')
      return null
    case 'failed':
      toast.error("GitHub didn't send back a complete confirmation. Try connecting again.")
      return null
  }
}

/** Connecting GitHub accounts and organisations, so sites can be published from their repositories (admins only). */
export function GitHubSection({ onSetPanelDomain }: { onSetPanelDomain?: () => void }) {
  const github = useResource(getGitHub, 'github')
  const query = useQuery()
  const [connecting, setConnecting] = useState(false)
  const [removed, setRemoved] = useState<GitHubRemoved | null>(null)
  const reload = github.reload

  const returned = query.has('github') ? query.toString() : ''
  useEffect(() => {
    const step = returned ? readGitHubReturn(new URLSearchParams(returned)) : undefined
    if (!step) return
    // The code in the address works once: take it out right away.
    clearQuery()
    if (handledReturns.has(returned)) return
    handledReturns.add(returned)
    finishReturn(step)
      .catch((error: unknown) => toast.error(errorMessage(error)))
      .finally(() => void reload())
  }, [returned, reload])

  async function remove(connection: GitHubConnection) {
    const yes = await confirm({
      title: `Remove the connection to ${connection.owner}?`,
      description:
        'The panel forgets its access to the repositories of this account. Published sites keep their versions. The app stays on GitHub until you delete it there.',
      confirmLabel: 'Remove connection',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      setRemoved(await removeConnection(connection.id))
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await reload()
  }

  async function checkAgain() {
    try {
      await refreshGitHub()
    } catch (error) {
      toast.error(errorMessage(error))
    }
    await reload()
  }

  const ready = github.status === 'ready' && github.data.ready
  const connections = github.status === 'ready' ? github.data.apps : []
  const connectButton = (another: boolean) => (
    <Button data-variant={another ? undefined : 'solid'} disabled={!ready} onClick={() => setConnecting(true)}>
      {another ? 'Add another account' : 'Connect GitHub'}
    </Button>
  )

  return (
    <Section
      title="GitHub"
      description="Connect a GitHub account or organisation so sites can be published from its repositories. Only administrators can use the connected repositories."
      actions={
        connections.length > 0 ? (
          <>
            <Button pendingAnnouncement="Checking" onClick={checkAgain}>
              Check again
            </Button>
            {connectButton(true)}
          </>
        ) : null
      }
    >
      {github.status === 'loading' ? <LoadingState label="Loading the GitHub connections…" /> : null}
      {github.status === 'error' ? (
        <ErrorState title="Couldn't load the GitHub connections" message={errorMessage(github.error)} onRetry={github.retry} />
      ) : null}
      {github.status === 'ready' && !github.data.ready ? (
        <div className="notice settings-notice">
          <p>{sentence(github.data.problem ?? '')}</p>
          {onSetPanelDomain ? <Button onClick={onSetPanelDomain}>Set panel domain</Button> : null}
        </div>
      ) : null}
      {github.status === 'ready' && connections.length === 0 ? (
        <EmptyState
          inSection
          title="GitHub is not connected"
          description="The panel creates its own app on your GitHub account or organisation. You confirm it on GitHub and choose which repositories it may see."
          action={connectButton(false)}
        />
      ) : null}
      {connections.length > 0 ? (
        <ul className="record-list" aria-label="Connected GitHub accounts">
          {connections.map((connection) => (
            <ConnectionRow key={connection.id} connection={connection} onRemove={remove} />
          ))}
        </ul>
      ) : null}
      <ConnectGitHubDialog
        open={connecting}
        onOpenChange={setConnecting}
        hasPersonal={connections.some((connection) => connection.owner_type === 'user')}
        another={connections.length > 0}
      />
      <RemovedDialog removed={removed} onClose={() => setRemoved(null)} />
    </Section>
  )
}
