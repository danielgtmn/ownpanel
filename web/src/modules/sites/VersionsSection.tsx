import { Button } from '@ui-sdk/react/button'
import { confirm } from '@ui-sdk/react/alert-dialog'
import { toast } from '@ui-sdk/react/toast'
import { errorMessage } from '../../shared/api/index.ts'
import { Section, StatusBadge } from '../../shared/ui/index.ts'
import { makeLive } from './api.ts'
import { formatReleaseDate, newestFirst, releaseTimestamp } from './releases.ts'
import type { Site } from './types.ts'

type VersionsSectionProps = {
  site: Site
  onChanged: () => Promise<void>
}

export function VersionsSection({ site, onChanged }: VersionsSectionProps) {
  const versions = newestFirst(site.releases)

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
      toast.error(errorMessage(error))
    }
    await onChanged()
  }

  return (
    <Section title="Versions" description="Every time you publish, the previous files are kept so you can go back.">
      {versions.length === 0 ? (
        <p className="muted">Nothing published yet. Upload your files above to create the first version.</p>
      ) : (
        <ul className="version-list" aria-label="Versions">
          {versions.map((release) => {
            const live = release === site.current_release
            const label = formatReleaseDate(release)
            return (
              <li key={release} className="version-row" data-live={live ? '' : undefined}>
                <div className="version-main">
                  <time dateTime={releaseTimestamp(release)}>{label}</time>
                  {live ? <StatusBadge tone="positive">Live</StatusBadge> : null}
                </div>
                {live ? null : (
                  <Button pendingAnnouncement="Switching version" onClick={() => switchTo(release)}>
                    Make live<span className="visually-hidden"> (version from {label})</span>
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Section>
  )
}
