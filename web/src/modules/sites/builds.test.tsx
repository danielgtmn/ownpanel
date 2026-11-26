import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import {
  checkVariables,
  confidenceLabel,
  environmentLabel,
  runFromQuery,
  settingsSummary,
  validVariableName,
  variableRows,
  variablesInput,
  type BuildSettings,
  type VariableRow,
} from './builds.ts'
import { BuildSettingsDialog } from './BuildSettingsDialog.tsx'
import { RunDetails, RunRow } from './RunsSection.tsx'
import type { Run } from './source.ts'

const settings: BuildSettings = {
  site: 'blog',
  configured: true,
  command: 'npm ci\nnpm run build',
  output: 'dist',
  image: 'node-24',
  variables: [
    { name: 'MODE', value: 'production', secret: false },
    { name: 'API_KEY', secret: true },
  ],
  images: ['node-24', 'node-22'],
  builds_available: true,
}

const row = (patch: Partial<VariableRow>): VariableRow => ({ key: 1, name: 'A', value: 'x', secret: false, stored: false, ...patch })

describe('build settings', () => {
  it('reads the environment and the confidence for people', () => {
    expect(environmentLabel('node-24')).toBe('Node 24')
    expect(environmentLabel('other')).toBe('other')
    expect(confidenceLabel('high')).toBe('Recognised')
    expect(confidenceLabel('none')).toBe('Not recognised')
  })

  it('sums the settings up in one line', () => {
    expect(settingsSummary(settings)).toBe('Runs 2 commands with Node 24, publishes the folder dist, 2 variables.')
    expect(settingsSummary({ ...settings, command: '', output: '.', variables: [] })).toBe(
      'Publishes the folder as it is, publishes the whole repository.',
    )
    expect(settingsSummary({ ...settings, configured: false })).toMatch(/^Not set up yet/)
  })

  it('checks variables before saving', () => {
    expect(validVariableName('API_URL')).toBe(true)
    expect(validVariableName('1X')).toBe(false)
    expect(validVariableName('lower')).toBe(false)
    expect(checkVariables([row({})])).toBeNull()
    expect(checkVariables([row({ name: '' })])).toBe('Every variable needs a name.')
    expect(checkVariables([row({}), row({ key: 2 })])).toBe('A is there twice.')
    expect(checkVariables([row({ value: '' })])).toBe('Enter a value for A.')
    expect(checkVariables([row({ value: '', stored: true, secret: true })])).toBeNull()
    expect(checkVariables([row({ value: 'abc', secret: true })])).toMatch(/at least 4/)
  })

  it('keeps stored values that were left empty', () => {
    const rows = variableRows(settings)
    expect(rows.map((r) => [r.name, r.value, r.stored])).toEqual([
      ['MODE', 'production', true],
      ['API_KEY', '', true],
    ])
    expect(variablesInput(rows)).toEqual([
      { name: 'MODE', value: 'production', secret: false },
      { name: 'API_KEY', secret: true },
    ])
    expect(variablesInput([{ ...rows[1], value: 'new-secret' }])).toEqual([{ name: 'API_KEY', value: 'new-secret', secret: true }])
  })

  it('opens only plain run ids from the address', () => {
    expect(runFromQuery(new URLSearchParams('run=06h1jxs7z5mac'))).toBe('06h1jxs7z5mac')
    expect(runFromQuery(new URLSearchParams('run=../x'))).toBeNull()
    expect(runFromQuery(new URLSearchParams(''))).toBeNull()
  })

  it('never puts a secret value into the dialog', () => {
    const html = renderToStaticMarkup(
      <BuildSettingsDialog open site="blog" settings={settings} onOpenChange={() => {}} onSaved={async () => {}} />,
    )
    expect(html).toContain('Build settings')
    expect(html).not.toMatch(/Slugify|Regex|Docker|container/i)
  })
})

const run: Run = {
  id: 'r1',
  site: 'blog',
  trigger: 'manual',
  status: 'running',
  repository: 'acme/website',
  ref: 'refs/heads/main',
  branch: 'main',
  commit: 'a'.repeat(40),
  short_commit: 'aaaaaaa',
  message: '',
  author_name: '',
  pusher: 'root',
  created_at: '2026-11-26T10:00:00Z',
  started_at: '2026-11-26T10:00:05Z',
}

describe('runs with builds', () => {
  it('offers to cancel a run that waits or runs', () => {
    const html = renderToStaticMarkup(<RunRow run={run} onOpen={() => {}} onCancel={() => {}} />)
    expect(html).toContain('Cancel')
    const done = renderToStaticMarkup(<RunRow run={{ ...run, status: 'succeeded' }} onOpen={() => {}} onCancel={() => {}} />)
    expect(done).not.toContain('Cancel')
  })

  it('names the published version and waits for the output of a queued run', () => {
    const html = renderToStaticMarkup(
      <RunDetails site="blog" run={{ ...run, status: 'succeeded', release: '20261126T100010Z-abc', finished_at: '2026-11-26T10:00:10Z' }} publishingAvailable />,
    )
    expect(html).toContain('Published as')
    expect(html).toContain('20261126T100010Z-abc')
    const queued = renderToStaticMarkup(<RunDetails site="blog" run={{ ...run, status: 'queued' }} publishingAvailable />)
    expect(queued).toContain('appears here once the build starts')
  })
})
