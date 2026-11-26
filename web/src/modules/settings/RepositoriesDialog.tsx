import { DialogClose } from '@ui-sdk/react/dialog'
import { errorMessage } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { hrefFor } from '../../shared/routing/index.ts'
import { ErrorState, ExternalIcon, FormDialog, LoadingState } from '../../shared/ui/index.ts'
import {
  listInstallationRepositories,
  sitesOf,
  sortRepositories,
  type GitHubInstallation,
  type GitHubLinkedSite,
  type GitHubRepositories,
} from './github.ts'

export function RepositoryList({
  data,
  installation,
}: {
  data: GitHubRepositories
  installation: Pick<GitHubInstallation, 'account' | 'settings_url' | 'sites'>
}) {
  if (data.repositories.length === 0) {
    return (
      <p className="muted">
        GitHub lets the panel see no repositories of {installation.account} right now.{' '}
        <a className="inline-link" href={installation.settings_url} target="_blank" rel="noreferrer noopener">
          Choose some on GitHub
          <ExternalIcon />
        </a>
      </p>
    )
  }
  const linked: GitHubLinkedSite[] = installation.sites
  return (
    <>
      <ul className="record-list github-repositories" aria-label={`Repositories of ${installation.account}`}>
        {sortRepositories(data.repositories, linked).map((repository) => {
          const sites = sitesOf(repository, linked)
          return (
            <li key={repository.id} className="record-row">
              <div className="record-main">
                <a className="inline-link record-title" href={repository.html_url} target="_blank" rel="noreferrer noopener">
                  {repository.full_name}
                  <ExternalIcon />
                </a>
                <ul className="record-facts">
                  <li>{repository.private ? 'Private' : 'Public'}</li>
                  {repository.archived ? <li>Archived</li> : null}
                  {repository.default_branch ? (
                    <li>
                      Default branch <span className="github-branch">{repository.default_branch}</span>
                    </li>
                  ) : null}
                </ul>
                {sites.length > 0 ? (
                  <p className="github-linked">
                    Linked to{' '}
                    {sites.map((site, index) => (
                      <span key={site.site}>
                        {index > 0 ? ', ' : ''}
                        <a href={hrefFor('sites', site.site)}>{site.site}</a> (branch{' '}
                        <span className="github-branch">{site.branch}</span>)
                      </span>
                    ))}
                  </p>
                ) : null}
              </div>
            </li>
          )
        })}
      </ul>
      {data.truncated ? (
        <p className="notice">Only the first {data.repositories.length} repositories are shown.</p>
      ) : null}
    </>
  )
}

function Repositories({ installation }: { installation: GitHubInstallation }) {
  const list = useResource(() => listInstallationRepositories(installation.id), `github-repositories:${installation.id}`)
  if (list.status === 'loading') return <LoadingState label="Loading the repositories…" />
  if (list.status === 'error') {
    return <ErrorState title="Couldn't load the repositories" message={errorMessage(list.error)} onRetry={list.retry} />
  }
  return <RepositoryList data={list.data} installation={installation} />
}

export function RepositoriesDialog({
  installation,
  onClose,
}: {
  installation: GitHubInstallation | null
  onClose: () => void
}) {
  return (
    <FormDialog
      open={installation !== null}
      onOpenChange={(open) => (open ? null : onClose())}
      title={installation ? `Repositories of ${installation.account}` : 'Repositories'}
      description="The repositories the panel may see. Sites can be linked to any of them."
    >
      <div className="github-repositories-body">{installation ? <Repositories installation={installation} /> : null}</div>
      <div className="form-actions">
        {installation ? (
          <a className="inline-link" href={installation.settings_url} target="_blank" rel="noreferrer noopener">
            Change on GitHub
            <ExternalIcon />
          </a>
        ) : null}
        <DialogClose>Close</DialogClose>
      </div>
    </FormDialog>
  )
}
