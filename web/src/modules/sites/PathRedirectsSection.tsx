import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import {
  createRedirect,
  deleteRedirect,
  errorMessage,
  isSavedButNotApplied,
  updateRedirect,
  type Redirect,
  type RedirectInput,
} from '../../shared/api/index.ts'
import type { Resource } from '../../shared/hooks/index.ts'
import { redirectFrom, RedirectDialog, RedirectSummary, sortRedirects } from '../../shared/redirects/index.ts'
import { EmptyState, ErrorState, LoadingState, Section } from '../../shared/ui/index.ts'
import type { Site } from './types.ts'

type Props = {
  site: Site
  redirects: Resource<Redirect[]>
  onChanged: () => Promise<void>
}

export function PathRedirectsSection({ site, redirects, onChanged }: Props) {
  const [dialog, setDialog] = useState<{ open: boolean; existing: Redirect | null }>({ open: false, existing: null })
  const hasDomains = site.domains.length > 0

  async function save(input: RedirectInput) {
    const existing = dialog.existing
    try {
      if (existing) await updateRedirect(existing.id, input)
      else await createRedirect(input)
      toast.success(`${input.source_domain}${input.source_path} now redirects to ${input.target}.`)
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setDialog((current) => ({ ...current, open: false }))
    await onChanged()
  }

  async function remove(redirect: Redirect) {
    const from = redirectFrom(redirect)
    const yes = await confirm({
      title: `Delete the redirect of ${from}?`,
      description: 'Visitors of this path see the site’s own page again, or a not-found page.',
      confirmLabel: 'Delete redirect',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteRedirect(redirect.id)
      toast.success(`${from} no longer redirects.`)
    } catch (error) {
      if (isSavedButNotApplied(error)) toast.warning(errorMessage(error))
      else toast.error(errorMessage(error))
    }
    await onChanged()
  }

  const addButton = hasDomains ? (
    <Button onClick={() => setDialog({ open: true, existing: null })}>Add redirect</Button>
  ) : null

  return (
    <Section
      title="Redirects"
      description="Send visitors who open one path, like /old, to another path or address."
      actions={redirects.status === 'ready' && redirects.data.length > 0 ? addButton : null}
    >
      {redirects.status === 'loading' ? <LoadingState label="Loading redirects…" /> : null}
      {redirects.status === 'error' ? (
        <ErrorState title="Couldn't load the redirects" message={errorMessage(redirects.error)} onRetry={redirects.retry} />
      ) : null}
      {redirects.status === 'ready' && redirects.data.length === 0 ? (
        hasDomains ? (
          <EmptyState
            title="No redirects yet"
            description="Moved a page? Add a redirect so old links keep working."
            action={addButton}
          />
        ) : (
          <p className="muted">Add a domain first. Redirects work on the site's domains.</p>
        )
      ) : null}
      {redirects.status === 'ready' && redirects.data.length > 0 ? (
        <ul className="record-list" aria-label={`Redirects of ${site.name}`}>
          {sortRedirects(redirects.data).map((redirect) => (
            <li className="record-row" key={redirect.id}>
              <RedirectSummary redirect={redirect} />
              <div className="record-actions">
                <Button onClick={() => setDialog({ open: true, existing: redirect })}>
                  Edit<span className="visually-hidden"> the redirect of {redirectFrom(redirect)}</span>
                </Button>
                <Button data-variant="destructive" pendingAnnouncement="Deleting" onClick={() => remove(redirect)}>
                  Delete<span className="visually-hidden"> the redirect of {redirectFrom(redirect)}</span>
                </Button>
              </div>
            </li>
          ))}
        </ul>
      ) : null}
      <RedirectDialog
        open={dialog.open}
        onOpenChange={(open) => setDialog((current) => ({ ...current, open }))}
        kind="path"
        existing={dialog.existing}
        domains={site.domains}
        onSubmit={save}
      />
    </Section>
  )
}
