import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { commitLink, runActive, runDuration, runNote, runTrigger } from './runs.ts'
import { RunDetails, RunRow } from './RunsSection.tsx'
import type { Run } from './source.ts'

const sha = '6113728f27ae82c7b1a177c8d03f9e96e0adf246'

const run: Run = {
  id: '01run',
  site: 'blog',
  trigger: 'push',
  status: 'queued',
  repository: 'acme/website',
  ref: 'refs/heads/main',
  branch: 'main',
  commit: sha,
  short_commit: '6113728',
  commit_url: `https://github.com/acme/website/commit/${sha}`,
  message: 'Fix <b>the</b> header',
  author_name: 'Ann Author',
  author_login: 'ann',
  pusher: 'ann',
  waiting: 'waits for building on the server, which this panel cannot do yet',
  created_at: '2026-11-26T10:00:00Z',
}

const open = () => undefined

describe('a run', () => {
  it('says what started it', () => {
    expect(runTrigger(run)).toBe('Push')
    expect(runTrigger({ ...run, trigger: 'manual' })).toBe('Started by hand')
    expect(runTrigger({ ...run, trigger: 'something-new' })).toBe('Other')
  })

  it('knows when it may still change', () => {
    expect(runActive(run)).toBe(true)
    expect(runActive({ ...run, status: 'running' })).toBe(true)
    for (const status of ['succeeded', 'failed', 'cancelled', 'skipped'] as const) {
      expect(runActive({ ...run, status }), status).toBe(false)
    }
  })

  it('links only to web pages', () => {
    expect(commitLink(run)).toBe(run.commit_url)
    expect(commitLink({ ...run, commit_url: undefined })).toBeNull()
    expect(commitLink({ ...run, commit_url: 'javascript:alert(1)' })).toBeNull()
    expect(commitLink({ ...run, commit_url: 'not a url' })).toBeNull()
  })

  it('says how long it took', () => {
    const at = (s: number) => new Date(Date.UTC(2026, 10, 26, 10, 0, s)).toISOString()
    expect(runDuration(run)).toBeNull()
    expect(runDuration({ ...run, started_at: at(0), finished_at: at(45) })).toBe('45 s')
    expect(runDuration({ ...run, started_at: at(0), finished_at: at(185) })).toBe('3 min 5 s')
    expect(runDuration({ ...run, started_at: at(0), finished_at: at(120) })).toBe('2 min')
    expect(runDuration({ ...run, started_at: at(0), finished_at: at(3600 + 600) })).toBe('1 h 10 min')
    expect(runDuration({ ...run, started_at: at(10), finished_at: at(0) })).toBeNull()
  })

  it('explains waiting and failures as sentences, but not a replaced push twice', () => {
    expect(runNote(run)).toBe('Waits until building on the server is available.')
    expect(runNote({ ...run, waiting: undefined })).toBeNull()
    expect(runNote({ ...run, status: 'failed', waiting: undefined, detail: 'the build ran out of time' })).toBe(
      'The build ran out of time.',
    )
    expect(runNote({ ...run, status: 'skipped', waiting: undefined, detail: 'replaced by a newer push' })).toBeNull()
  })
})

describe('the list of runs', () => {
  it('shows commit, message as text, author, trigger, status and time', () => {
    const html = renderToStaticMarkup(<RunRow run={run} onOpen={open} />)
    expect(html).toContain(`href="https://github.com/acme/website/commit/${sha}"`)
    expect(html).toContain('>6113728</code>')
    expect(html).toContain('Fix &lt;b&gt;the&lt;/b&gt; header')
    expect(html).not.toContain('<b>the</b>')
    expect(html).toContain('Ann Author (ann)')
    expect(html).toContain('<li>Push</li>')
    expect(html).toContain('Waiting')
    expect(html).toContain('dateTime="2026-11-26T10:00:00.000Z"')
    expect(html).toMatch(/<button[^>]*>Details<span class="visually-hidden"> of the build of 6113728<\/span><\/button>/)
    expect(html).not.toMatch(/\b(webhook|queued|sha)\b/i)
  })

  it('shows the commit without a link when its page is unknown, and says when there is no message', () => {
    const html = renderToStaticMarkup(<RunRow run={{ ...run, commit_url: undefined, message: '' }} onOpen={open} />)
    expect(html).not.toContain('<a ')
    expect(html).toContain('No message')
  })

  it('reads well for every status', () => {
    const labels = {
      queued: 'Waiting',
      running: 'Building',
      succeeded: 'Published',
      failed: 'Failed',
      cancelled: 'Cancelled',
      skipped: 'Replaced by a newer build',
    } as const
    for (const [status, label] of Object.entries(labels)) {
      const html = renderToStaticMarkup(<RunRow run={{ ...run, status: status as Run['status'] }} onOpen={open} />)
      expect(html, status).toContain(label)
    }
  })
})

describe('the details of a run', () => {
  it('lists everything known and keeps a place for the output', () => {
    const html = renderToStaticMarkup(<RunDetails site="blog" run={run} publishingAvailable={false} />)
    for (const text of ['Commit', 'Message', 'Author', 'Repository', 'acme/website', 'Branch', 'Started by', 'Received']) {
      expect(html, text).toContain(text)
    }
    // The pusher is the author: not shown twice.
    expect(html).not.toContain('Pushed by')
    expect(html).toContain('aria-label="Output of the build"')
    expect(html).toContain('appears here once building on the server is available')
  })

  it('shows who pushed when that was someone else, and when it ran', () => {
    const html = renderToStaticMarkup(
      <RunDetails
        site="blog"
        run={{
          ...run,
          status: 'failed',
          waiting: undefined,
          pusher: 'bob',
          detail: 'the build ran out of time',
          started_at: '2026-11-26T10:00:05Z',
          finished_at: '2026-11-26T10:01:05Z',
        }}
        publishingAvailable
      />,
    )
    expect(html).toContain('Pushed by')
    expect(html).toContain('bob')
    expect(html).toContain('The build ran out of time.')
    expect(html).toContain('(took 1 min)')
    // The log itself is loaded once the dialog shows.
    expect(html).toContain('Loading the output…')
  })
})
