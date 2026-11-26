import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { BuildsStatus } from './BuildsSection.tsx'
import { activityText, buildSummary, formatDuration, formatSize, type BuildAvailability } from './builds.ts'

const ready: BuildAvailability = {
  available: true,
  slots: 2,
  running: 1,
  waiting: 0,
  max_memory_bytes: 4 * 1024 ** 3,
  max_cpu_millis: 4000,
  max_seconds: 3600,
  workspace_bytes: 4 * 1024 ** 3,
  cache_bytes: 1024 ** 3,
  disk_bytes: 19 * 1024 ** 3,
  free_bytes: 18 * 1024 ** 3,
  images: ['node-24', 'node-22'],
}

describe('build availability', () => {
  it('says the server can build, with the limits of a build', () => {
    const html = renderToStaticMarkup(<BuildsStatus info={ready} />)
    expect(html).toContain('This server can build sites')
    expect(html).toContain('Up to 2 builds at a time.')
    expect(html).toContain('4 GB of memory, 4 GB of disk space and an hour')
    expect(html).toContain('Right now: 1 running.')
  })

  it('says why not, in words for people', () => {
    for (const reason of ['off', 'system', 'runtime', 'install', 'limits', 'disk', 'network', 'images', 'worker', 'not_configured', 'something-new']) {
      const summary = buildSummary({ ...ready, available: false, reason, detail: 'podman 3.4.4 nft cgroup' })
      expect(summary.tone).toBe('warning')
      expect(summary.title).toBe("This server can't build sites")
      expect(summary.text.length).toBeGreaterThan(20)
      // No technical detail or names of programs reach the page.
      expect(summary.text).not.toMatch(/podman|nft|cgroup|container|namespace/i)
    }
    expect(buildSummary({ ...ready, available: false, reason: 'preparing' }).tone).toBe('neutral')
  })

  it('formats sizes, durations and what runs now', () => {
    expect(formatSize(512 * 1024 ** 2)).toBe('512 MB')
    expect(formatSize(1.5 * 1024 ** 3)).toBe('1.5 GB')
    expect(formatDuration(900)).toBe('15 minutes')
    expect(formatDuration(7200)).toBe('2 hours')
    expect(activityText({ ...ready, running: 0 })).toBe('')
    expect(activityText({ ...ready, running: 2, waiting: 3 })).toBe('2 running, 3 waiting')
  })
})
