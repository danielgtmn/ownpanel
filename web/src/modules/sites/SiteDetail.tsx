import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import {
  errorMessage,
  isNotFound,
  isSavedButNotApplied,
  listRedirects,
  listSiteRedirects,
} from '../../shared/api/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { hrefFor, navigate } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import {
  ArrowLeftIcon,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  Section,
  StatusBadge,
} from '../../shared/ui/index.ts'
import { deleteSite, getSite, saveSite } from './api.ts'
import { DomainsStatus } from './DomainsStatus.tsx'
import { MainAddressSection } from './MainAddressSection.tsx'
import { PathRedirectsSection } from './PathRedirectsSection.tsx'
import { PublishSection } from './PublishSection.tsx'
import { formatReleaseDate, hasContent } from './releases.ts'
import { SiteFormDialog } from './SiteFormDialog.tsx'
import { VersionsSection } from './VersionsSection.tsx'
import type { Site } from './types.ts'

const backLink = (
  <a className="inline-link" href={hrefFor('sites')}>
    <ArrowLeftIcon />
    All sites
  </a>
)

function Loaded({ site, reload }: { site: Site; reload: () => Promise<void> }) {
  const [editing, setEditing] = useState(false)
  const { isAdmin } = useSession()
  const live = hasContent(site)
  const domainKey = site.domains.join(',')
  const redirects = useResource(listRedirects, `redirects:${site.name}:${domainKey}`)
  const pathRedirects = useResource(() => listSiteRedirects(site.name), `site-redirects:${site.name}:${domainKey}`)

  async function redirectsChanged() {
    await Promise.all([redirects.reload(), pathRedirects.reload()])
  }

  async function mainAddressChanged() {
    await Promise.all([reload(), redirectsChanged()])
  }

  async function saveDomains(_name: string, domains: string[]) {
    try {
      await saveSite(site.name, domains)
      toast.success('Domains saved.')
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setEditing(false)
    await Promise.all([reload(), redirectsChanged()])
  }

  async function remove() {
    const yes = await confirm({
      title: `Delete ${site.name}?`,
      description: 'The site goes offline and all its files and versions are deleted. This can\'t be undone.',
      confirmLabel: 'Delete site',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteSite(site.name)
      toast.success(`${site.name} was deleted.`)
    } catch (error) {
      if (isSavedButNotApplied(error)) {
        toast.warning(errorMessage(error))
      } else {
        toast.error(errorMessage(error))
        await reload().catch(() => undefined)
        return
      }
    }
    navigate('sites')
  }

  return (
    <>
      <PageHeader
        back={backLink}
        title={site.name}
        meta={
          <>
            <StatusBadge tone={live ? 'positive' : 'neutral'}>{live ? 'Live' : 'No content yet'}</StatusBadge>
            {live ? <span className="muted">Published {formatReleaseDate(site.current_release)}</span> : null}
            {isAdmin ? <span className="muted">Owner: {site.owner}</span> : null}
          </>
        }
      />
      <PublishSection name={site.name} previewAddresses={site.preview_addresses === true} onPublished={reload} />
      <VersionsSection site={site} onChanged={reload} />
      <Section
        title="Domains"
        description="Visitors reach this site on these domains."
        actions={
          <Button onClick={() => setEditing(true)}>
            Change<span className="visually-hidden"> domains</span>
          </Button>
        }
      >
        {site.domains.length > 0 ? (
          <DomainsStatus siteName={site.name} domains={site.domains} />
        ) : (
          <p className="muted">No domains yet. Add one so visitors can find your site.</p>
        )}
      </Section>
      <MainAddressSection
        site={site}
        redirects={redirects}
        pathRedirects={pathRedirects.status === 'ready' ? pathRedirects.data : []}
        onChanged={mainAddressChanged}
      />
      <PathRedirectsSection site={site} redirects={pathRedirects} onChanged={redirectsChanged} />
      <Section
        title="Delete site"
        description="Takes the site offline and removes its files and all versions."
        tone="danger"
      >
        <Button data-variant="destructive" onClick={remove}>
          Delete site
        </Button>
      </Section>
      <SiteFormDialog
        open={editing}
        onOpenChange={setEditing}
        mode="edit"
        initialName={site.name}
        initialDomains={site.domains}
        onSubmit={saveDomains}
      />
    </>
  )
}

export function SiteDetail({ name }: { name: string }) {
  const site = useResource(() => getSite(name), `site:${name}`)
  usePageTitle(site.status === 'ready' ? site.data.name : null)

  if (site.status === 'loading') return <LoadingState label="Loading site…" />
  if (site.status === 'error') {
    if (isNotFound(site.error)) {
      return (
        <EmptyState
          title="Site not found"
          description={`There's no site called "${name}". It may have been deleted.`}
          action={<a href={hrefFor('sites')}>Back to all sites</a>}
        />
      )
    }
    return (
      <>
        {backLink}
        <ErrorState title="Couldn't load this site" message={errorMessage(site.error)} onRetry={site.retry} />
      </>
    )
  }
  return <Loaded site={site.data} reload={site.reload} />
}
