import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import {
  createRedirect,
  deleteRedirect,
  errorMessage,
  isSavedButNotApplied,
  listRedirects,
  updateRedirect,
  type Redirect,
  type RedirectInput,
} from '../../shared/api/index.ts'
import { DomainCheck } from '../../shared/domains/index.ts'
import { registrableDomain } from '../../shared/families/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { domainRedirects, RedirectDialog, RedirectSummary, sortRedirects } from '../../shared/redirects/index.ts'
import { clearQuery, hrefFor, prefilledDomain, useQuery } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { EmptyState, ErrorState, LoadingState, PageHeader, Section } from '../../shared/ui/index.ts'

type RowProps = {
  redirect: Redirect
  showOwner: boolean
  onEdit: (redirect: Redirect) => void
  onDelete: (redirect: Redirect) => Promise<void>
}

function RedirectRow({ redirect, showOwner, onEdit, onDelete }: RowProps) {
  const from = redirect.source_domain
  return (
    <li className="record-row">
      <RedirectSummary redirect={redirect}>
        {showOwner ? <p className="muted redirect-owner">Owner: {redirect.owner || 'nobody'}</p> : null}
        <DomainCheck domain={from} />
      </RedirectSummary>
      <div className="record-actions">
        <Button onClick={() => onEdit(redirect)}>
          Edit<span className="visually-hidden"> the redirect of {from}</span>
        </Button>
        <Button data-variant="destructive" pendingAnnouncement="Deleting" onClick={() => onDelete(redirect)}>
          Delete<span className="visually-hidden"> the redirect of {from}</span>
        </Button>
      </div>
    </li>
  )
}

export function RedirectsPage() {
  const redirects = useResource(listRedirects, 'redirects')
  const { isAdmin } = useSession()
  const [dialog, setDialog] = useState<{ open: boolean; existing: Redirect | null }>({ open: false, existing: null })
  const query = useQuery()
  const family = query.get('family')
  const wantsNew = query.has('new')
  const prefilled = wantsNew ? prefilledDomain(query) : undefined
  usePageTitle(family ? `Redirects of ${family}` : 'Redirects')

  async function save(input: RedirectInput) {
    const existing = dialog.existing
    try {
      if (existing) await updateRedirect(existing.id, input)
      else await createRedirect(input)
      toast.success(`${input.source_domain} now redirects to ${input.target}.`)
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setDialog((current) => ({ ...current, open: false }))
    clearQuery()
    await redirects.reload()
  }

  async function remove(redirect: Redirect) {
    const yes = await confirm({
      title: `Delete the redirect of ${redirect.source_domain}?`,
      description: 'Visitors of this domain are no longer sent on, and it stops working here.',
      confirmLabel: 'Delete redirect',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteRedirect(redirect.id)
      toast.success(`${redirect.source_domain} no longer redirects.`)
    } catch (error) {
      if (isSavedButNotApplied(error)) toast.warning(errorMessage(error))
      else toast.error(errorMessage(error))
    }
    await redirects.reload()
  }

  const addButton = (
    <Button data-variant="solid" onClick={() => setDialog({ open: true, existing: null })}>
      Add redirect
    </Button>
  )
  const all = redirects.status === 'ready' ? sortRedirects(domainRedirects(redirects.data)) : []
  const list = family ? all.filter((redirect) => registrableDomain(redirect.source_domain) === family) : all

  return (
    <>
      <PageHeader
        title="Redirects"
        description="Send everyone who opens a domain to another address."
        actions={all.length > 0 ? addButton : null}
      />
      {family ? (
        <p className="notice" role="status">
          Showing the redirects of {family} and its subdomains. <a href={hrefFor('redirects')}>Show all redirects</a>
        </p>
      ) : null}
      <Section
        title="Domain redirects"
        description="The domain has to point to this server. Redirects for single paths of a site are on the site's page."
      >
        {redirects.status === 'loading' ? <LoadingState label="Loading redirects…" /> : null}
        {redirects.status === 'error' ? (
          <ErrorState title="Couldn't load the redirects" message={errorMessage(redirects.error)} onRetry={redirects.retry} />
        ) : null}
        {redirects.status === 'ready' && family && all.length > 0 && list.length === 0 ? (
          <EmptyState inSection title={`No redirects on ${family}`} description="None of its domains redirects yet." action={addButton} />
        ) : null}
        {redirects.status === 'ready' && all.length === 0 ? (
          <EmptyState
            inSection
            title="No redirects yet"
            description="Have an old domain or a short one? Send its visitors to where your content lives now."
            action={addButton}
          />
        ) : null}
        {list.length > 0 ? (
          <ul className="record-list" aria-label="Domain redirects">
            {list.map((redirect) => (
              <RedirectRow
                key={redirect.id}
                redirect={redirect}
                showOwner={isAdmin}
                onEdit={(existing) => setDialog({ open: true, existing })}
                onDelete={remove}
              />
            ))}
          </ul>
        ) : null}
      </Section>
      <RedirectDialog
        open={dialog.open || wantsNew}
        onOpenChange={(open) => {
          setDialog((current) => ({ ...current, open }))
          if (!open) clearQuery()
        }}
        kind="domain"
        existing={dialog.existing}
        initialDomain={dialog.existing ? undefined : prefilled}
        domains={[]}
        onSubmit={save}
      />
    </>
  )
}
