import { describe, expect, it } from 'vitest'
import {
  branchChoices,
  initialBranch,
  repositoryKey,
  runAuthor,
  runStatus,
  sourceProblem,
  type Run,
  type SiteSource,
} from './source.ts'

const source = (patch: Partial<SiteSource>): SiteSource => ({
  kind: 'github',
  repository: 'acme/site',
  branch: 'main',
  publish_on_push: true,
  status: 'linked',
  linked_by: 'root',
  linked_at: '2026-11-26T10:00:00Z',
  ...patch,
})

describe('sourceProblem', () => {
  it('is quiet when linked', () => {
    expect(sourceProblem(source({}))).toBeNull()
  })
  it('explains every other state', () => {
    expect(sourceProblem(source({ status: 'connection_missing' }))).toMatch(/connection .* was removed/)
    expect(sourceProblem(source({ status: 'suspended', account: 'acme' }))).toMatch(/paused on GitHub for acme/)
    expect(sourceProblem(source({ status: 'repository_missing' }))).toMatch(/no longer see/)
    expect(sourceProblem(source({ status: 'unchecked', problem: 'GitHub did not answer in time' }))).toBe(
      "Couldn't check the repository: GitHub did not answer in time",
    )
    expect(sourceProblem(source({ status: 'unchecked' }))).toBe("Couldn't check the repository right now.")
  })
})

describe('branches', () => {
  const list = { branches: ['dev', 'main', 'release/2.0'], default_branch: 'main', truncated: false }
  it('puts the current and default branch first, without duplicates', () => {
    expect(branchChoices(list, 'release/2.0')).toEqual(['release/2.0', 'main', 'dev'])
    expect(branchChoices(list)).toEqual(['main', 'dev', 'release/2.0'])
  })
  it('preselects the current branch while it exists', () => {
    expect(initialBranch(list, 'dev')).toBe('dev')
    expect(initialBranch(list, 'gone')).toBe('main')
    expect(initialBranch({ branches: ['x'], default_branch: 'main', truncated: false })).toBe('x')
    expect(initialBranch({ branches: [], default_branch: 'main', truncated: false })).toBe('')
  })
})

it('keys repositories by installation and id', () => {
  expect(repositoryKey({ installation_id: 3, id: 7 })).toBe('3:7')
  expect(repositoryKey({ installation_id: 3, repository_id: 7 })).toBe('3:7')
})

const run = (patch: Partial<Run>): Run => ({
  id: 1,
  site: 'blog',
  trigger: 'push',
  status: 'queued',
  repository: 'acme/site',
  ref: 'refs/heads/main',
  branch: 'main',
  commit: '6113728f27ae82c7b1a177c8d03f9e96e0adf246',
  short_commit: '6113728',
  message: 'Fix the header',
  author_name: 'Ann Author',
  author_login: 'ann',
  created_at: '2026-11-26T10:00:00Z',
  ...patch,
})

describe('runs', () => {
  it('names every status', () => {
    expect(runStatus(run({}))).toEqual({ label: 'Waiting', tone: 'neutral' })
    expect(runStatus(run({ status: 'succeeded' })).tone).toBe('positive')
    expect(runStatus(run({ status: 'failed' })).tone).toBe('warning')
    expect(runStatus(run({ status: 'skipped' })).label).toBe('Replaced by a newer push')
  })
  it('names the author', () => {
    expect(runAuthor(run({}))).toBe('Ann Author (ann)')
    expect(runAuthor(run({ author_name: 'ann' }))).toBe('ann')
    expect(runAuthor(run({ author_name: '', author_login: undefined, pusher: 'bob' }))).toBe('bob')
  })
})
