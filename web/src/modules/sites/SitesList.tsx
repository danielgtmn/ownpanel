import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage, isSavedButNotApplied } from '../../shared/api/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { hrefFor, navigate } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { EmptyState, ErrorState, LoadingState, PageHeader, StatusBadge } from '../../shared/ui/index.ts'
import { listSites, saveSite } from './api.ts'
import { formatReleaseDate, hasContent, releaseTimestamp } from './releases.ts'
import { SiteFormDialog } from './SiteFormDialog.tsx'
import type { Site } from './types.ts'

function SiteRow({ site, showOwner }: { site: Site; showOwner: boolean }) {
  const live = hasContent(site)
  return (
    <li className="site-row">
      <div className="site-row-main">
        <a className="site-row-name" href={hrefFor('sites', site.name)}>
          {site.name}
        </a>
        {showOwner ? <span className="muted">Owner: {site.owner}</span> : null}
        {site.domains.length > 0 ? (
          <ul className="chip-list" aria-label={`Domains of ${site.name}`}>
            {site.domains.map((domain) => (
              <li key={domain}>{domain}</li>
            ))}
          </ul>
        ) : (
          <span className="muted">No domains yet</span>
        )}
      </div>
      <div className="site-row-status">
        <StatusBadge tone={live ? 'positive' : 'neutral'}>{live ? 'Live' : 'No content yet'}</StatusBadge>
        {live ? (
          <span className="muted">
            Published <time dateTime={releaseTimestamp(site.current_release)}>{formatReleaseDate(site.current_release)}</time>
          </span>
        ) : null}
      </div>
    </li>
  )
}

export function SitesList() {
  const sites = useResource(listSites, 'sites')
  const { isAdmin } = useSession()
  const [creating, setCreating] = useState(false)
  usePageTitle('Sites')

  const newSiteButton = (
    <Button data-variant="solid" onClick={() => setCreating(true)}>
      New site
    </Button>
  )

  async function create(name: string, domains: string[]) {
    try {
      await saveSite(name, domains)
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setCreating(false)
    navigate('sites', name)
  }

  return (
    <>
      <PageHeader
        title="Sites"
        description={isAdmin ? 'Every site on this server.' : 'Everything you publish on this server.'}
        actions={sites.status === 'ready' && sites.data.length > 0 ? newSiteButton : null}
      />
      {sites.status === 'loading' ? <LoadingState label="Loading your sites…" /> : null}
      {sites.status === 'error' ? (
        <ErrorState title="Couldn't load your sites" message={errorMessage(sites.error)} onRetry={sites.retry} />
      ) : null}
      {sites.status === 'ready' && sites.data.length === 0 ? (
        <EmptyState
          title="No sites yet"
          description="Create your first site, then upload its files to put it online."
          action={newSiteButton}
        />
      ) : null}
      {sites.status === 'ready' && sites.data.length > 0 ? (
        <ul className="site-list" aria-label="Your sites">
          {sites.data.map((site) => (
            <SiteRow key={site.name} site={site} showOwner={isAdmin} />
          ))}
        </ul>
      ) : null}
      <SiteFormDialog
        open={creating}
        onOpenChange={setCreating}
        mode="create"
        takenNames={sites.status === 'ready' ? sites.data.map((site) => site.name) : []}
        onSubmit={create}
      />
    </>
  )
}
