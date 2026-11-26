import { request } from '../../shared/api/index.ts'

export type SourceStatus = 'linked' | 'connection_missing' | 'suspended' | 'repository_missing' | 'unchecked'

export type SiteSource = {
  kind: 'github' | 'git'
  repository: string
  repository_id?: number
  installation_id?: number
  account?: string
  url?: string
  branch: string
  publish_on_push: boolean
  status: SourceStatus
  problem?: string
  linked_by: string
  linked_at: string
}

export type RunStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'skipped'

/**
 * One push (later also "Build now") that should build and publish the site.
 * message, author_name and pusher come from the repository: show them as
 * text only.
 */
export type Run = {
  id: string
  site: string
  trigger: string
  status: RunStatus
  repository: string
  ref: string
  branch?: string
  commit: string
  short_commit: string
  /** The commit's page on GitHub, if known. */
  commit_url?: string
  message: string
  author_name: string
  author_login?: string
  pusher?: string
  detail?: string
  /** Set while a queued run waits because the server cannot build yet. */
  waiting?: string
  created_at: string
  started_at?: string
  finished_at?: string
}

export type SiteSourceState = {
  site: string
  source: SiteSource | null
  /** False while pushes are not built and published on the server yet. */
  publishing_available: boolean
  /** The newest run, null if there is none. */
  last_run: Run | null
}

export type LinkableRepository = {
  id: number
  installation_id: number
  owner: string
  name: string
  full_name: string
  private: boolean
  archived: boolean
  default_branch: string
  html_url: string
  account: string
}

export type LinkableRepositories = {
  connected: boolean
  repositories: LinkableRepository[]
  problems: string[]
  truncated: boolean
}

export type SourceBranches = { branches: string[]; default_branch: string; truncated: boolean }

export type SourceInput = {
  installation_id: number
  repository_id: number
  branch: string
  publish_on_push: boolean
}

const sourcePath = (site: string) => `/sites/${encodeURIComponent(site)}/source`

export const getSiteSource = (site: string) => request<SiteSourceState>(sourcePath(site))

export const setSiteSource = (site: string, input: SourceInput) =>
  request<SiteSourceState>(sourcePath(site), { method: 'PUT', body: input })

export const removeSiteSource = (site: string) => request<void>(sourcePath(site), { method: 'DELETE' })

export const listLinkableRepositories = (site: string) =>
  request<LinkableRepositories>(`${sourcePath(site)}/repositories`)

export const listSourceBranches = (site: string, repository: LinkableRepository) => {
  const query = new URLSearchParams({
    installation_id: String(repository.installation_id),
    repository_id: String(repository.id),
  })
  return request<SourceBranches>(`${sourcePath(site)}/branches?${query.toString()}`)
}

/** A key that names one repository of one installation, for selects. */
export const repositoryKey = (r: { installation_id?: number; id?: number; repository_id?: number }) =>
  `${r.installation_id ?? 0}:${r.id ?? r.repository_id ?? 0}`

/** What is wrong with a linked source, for people; null when nothing is. */
export function sourceProblem(source: SiteSource): string | null {
  switch (source.status) {
    case 'linked':
      return null
    case 'connection_missing':
      return 'The GitHub connection this repository came through was removed. Link the site again or unlink it.'
    case 'suspended':
      return `Access is paused on GitHub${source.account ? ` for ${source.account}` : ''}, so the panel can't reach this repository.`
    case 'repository_missing':
      return 'The panel can no longer see this repository. Allow it again on GitHub, or link another one.'
    case 'unchecked':
      return source.problem ? `Couldn't check the repository: ${source.problem}` : "Couldn't check the repository right now."
  }
}

/** How a run's status reads for people, and the tone of its badge. */
export function runStatus(run: Run): { label: string; tone: 'positive' | 'neutral' | 'warning' } {
  switch (run.status) {
    case 'queued':
      return { label: 'Waiting', tone: 'neutral' }
    case 'running':
      return { label: 'Building', tone: 'neutral' }
    case 'succeeded':
      return { label: 'Published', tone: 'positive' }
    case 'failed':
      return { label: 'Failed', tone: 'warning' }
    case 'cancelled':
      return { label: 'Cancelled', tone: 'neutral' }
    case 'skipped':
      return { label: 'Replaced by a newer push', tone: 'neutral' }
  }
}

/** Who made the change: the author's name, with their account if it differs. */
export function runAuthor(run: Run): string {
  const name = run.author_name || run.author_login || run.pusher || ''
  if (run.author_login && run.author_name && run.author_login !== run.author_name) {
    return `${run.author_name} (${run.author_login})`
  }
  return name
}

/** Branches to offer: the list, with the current and default branch first. */
export function branchChoices(list: SourceBranches, current?: string): string[] {
  const out: string[] = []
  for (const name of [current, list.default_branch, ...list.branches]) {
    if (name && !out.includes(name)) out.push(name)
  }
  return out
}

/** The branch to preselect: the current one if it still exists, else the default branch. */
export function initialBranch(list: SourceBranches, current?: string): string {
  if (current && list.branches.includes(current)) return current
  if (list.branches.includes(list.default_branch)) return list.default_branch
  return list.branches[0] ?? ''
}
