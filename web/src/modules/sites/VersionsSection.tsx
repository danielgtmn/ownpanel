import { Button } from '@ui-sdk/react/button'
import { confirm } from '@ui-sdk/react/alert-dialog'
import { toast } from '@ui-sdk/react/toast'
import { errorMessage, isSavedButNotApplied } from '../../shared/api/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { Section, StatusBadge } from '../../shared/ui/index.ts'
import { makeLive } from './api.ts'
import { formatReleaseDate, releaseTimestamp, siteVersions, versionState } from './releases.ts'
import type { Site } from './types.ts'

type VersionsSectionProps = {
  site: Site
  onChanged: () => Promise<void>
}

function NoPreviewAddresses() {
  const { isAdmin } = useSession()
  return isAdmin ? (
    <p className="muted">
      To open a version before it goes live, set a domain for previews in{' '}
      <a className="inline-link" href="#/settings">
        Settings
      </a>
      .
    </p>
  ) : (
    <p className="muted">To open a version before it goes live, an administrator has to set a domain for previews.</p>
  )
}

export function VersionsSection({ site, onChanged }: VersionsSectionProps) {
  const versions = siteVersions(site)

  async function switchTo(release: string) {
    const label = formatReleaseDate(release)
    const yes = await confirm({
      title: 'Make this version live?',
      description: `Visitors will see the version published on ${label}.`,
      confirmLabel: 'Make live',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await makeLive(site.name, release)
      toast.success(`The version from ${label} is live.`)
    } catch (error) {
      if (isSavedButNotApplied(error)) toast.warning(errorMessage(error))
      else toast.error(errorMessage(error))
    }
    await onChanged()
  }

  return (
    <Section
      title="Versions"
      description="Every time you publish, the previous files are kept so you can go back. A version you publish without making it live waits here until you make it live."
    >
      {versions.length === 0 ? (
        <p className="muted">Nothing published yet. Upload your files above to create the first version.</p>
      ) : (
        <>
          <ul className="version-list" aria-label="Versions">
            {versions.map((version) => {
              const label = formatReleaseDate(version.id)
              const state = versionState(version)
              return (
                <li key={version.id} className="version-row" data-live={version.live ? '' : undefined}>
                  <div className="version-main">
                    <time dateTime={releaseTimestamp(version.id)}>{label}</time>
                    <StatusBadge tone={state.tone}>{state.text}</StatusBadge>
                  </div>
                  <div className="version-actions">
                    {version.preview_url ? (
                      <a className="inline-link" href={version.preview_url} target="_blank" rel="noreferrer noopener">
                        Open preview
                        <span className="visually-hidden"> of the version from {label} (opens in a new tab)</span>
                      </a>
                    ) : null}
                    {version.live ? null : (
                      <Button pendingAnnouncement="Switching version" onClick={() => switchTo(version.id)}>
                        Make live<span className="visually-hidden"> (version from {label})</span>
                      </Button>
                    )}
                  </div>
                </li>
              )
            })}
          </ul>
          {site.preview_addresses ? (
            <p className="muted">Anyone with a preview link can open that version, so share it only with people who should see it.</p>
          ) : (
            <NoPreviewAddresses />
          )}
        </>
      )}
    </Section>
  )
}
