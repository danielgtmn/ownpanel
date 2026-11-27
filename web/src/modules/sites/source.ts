import { request } from '../../shared/api/index.ts'

export type SourceStatus =
  | 'linked'
  | 'connection_missing'
  | 'suspended'
  | 'repository_missing'
  | 'unchecked'
  | 'not_checked'
  | 'host_key_changed'
  | 'access_failed'

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
  /** Pull requests against the branch from the same repository are built as previews. */
  pull_request_previews: boolean
  /** Pull requests from forks are built too, without the site's variables. */
  fork_previews: boolean
  /** Kind git: the server (with its port if not the default) and how it is reached. */
  server?: string
  transport?: 'ssh' | 'https'
  /** The public half of the site's deploy key, to add at the Git host (SSH only). */
  deploy_key?: string
  /** Fingerprint of the confirmed server key, its type, and a different one the server shows now. */
  host_key?: string
  host_key_type?: string
  new_host_key?: string
  /** Where the Git host reports pushes; empty while the panel has no domain with HTTPS. */
  notify_url?: string
  notify_problem?: string
  checked_at?: string
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
  /** The version a succeeded run published. */
  release?: string
  /** Runs of pull requests (trigger pull_request): its number and page. */
  pull_request?: number
  pull_request_url?: string
  /** The pull request comes from a fork: built without the site's variables. */
  fork?: boolean
  /** owner/name of the fork. */
  head_repository?: string
  /** The preview the run published, while it exists. Carries the key that opens it. */
  preview_url?: string
  /** The preview a succeeded run of a pull request published is gone (replaced or the pull request closed). */
  preview_gone?: boolean
}

export type SiteSourceState = {
  site: string
  source: SiteSource | null
  /** False while pushes are not built and published on the server yet. */
  publishing_available: boolean
  /** The newest run, null if there is none. */
  last_run: Run | null
  /** The secret of the notification address, only in the answer that made it. */
  notify_secret?: string
}

export type GitRepositoryCheck = {
  repository: string
  server: string
  transport: 'ssh' | 'https'
  host_key?: string
  host_key_type?: string
  /** The site already trusts this key of this server. */
  pinned?: boolean
  branches?: string[]
  default_branch?: string
  truncated?: boolean
}

export type GitSourceInput = {
  repository: string
  branch: string
  publish_on_push: boolean
  host_key?: string
}

export type GitSourceCheck = SiteSourceState & {
  ok: boolean
  branches: string[]
  default_branch?: string
  truncated?: boolean
  branch_found: boolean
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

export type PreviewsInput = { pull_requests: boolean; forks: boolean }

const sourcePath = (site: string) => `/sites/${encodeURIComponent(site)}/source`

export const setSitePreviews = (site: string, input: PreviewsInput) =>
  request<SiteSourceState>(`${sourcePath(site)}/previews`, { method: 'PUT', body: input })

/** Which pull requests get previews, in a few words. */
export function previewsSummary(source: SiteSource): string {
  if (!source.pull_request_previews) return 'Pull requests: no previews'
  return source.fork_previews ? 'Pull requests: previews, also from forks' : 'Pull requests: previews'
}

export const getSiteSource = (site: string) => request<SiteSourceState>(sourcePath(site))

export const setSiteSource = (site: string, input: SourceInput) =>
  request<SiteSourceState>(sourcePath(site), { method: 'PUT', body: input })

export const removeSiteSource = (site: string) => request<void>(sourcePath(site), { method: 'DELETE' })

const gitPath = (site: string) => `${sourcePath(site)}/git`

export const inspectGitRepository = (site: string, repository: string) =>
  request<GitRepositoryCheck>(`${gitPath(site)}/inspect`, { method: 'POST', body: { repository } })

export const setGitSource = (site: string, input: GitSourceInput) =>
  request<SiteSourceState>(gitPath(site), { method: 'PUT', body: input })

export const checkGitSource = (site: string) => request<GitSourceCheck>(`${gitPath(site)}/check`, { method: 'POST' })

export const acceptGitHostKey = (site: string, hostKey: string) =>
  request<SiteSourceState>(`${gitPath(site)}/host-key`, { method: 'POST', body: { host_key: hostKey } })

export const replaceDeployKey = (site: string) =>
  request<SiteSourceState>(`${gitPath(site)}/deploy-key`, { method: 'POST' })

export const replaceNotifySecret = (site: string) =>
  request<SiteSourceState>(`${gitPath(site)}/notify-secret`, { method: 'POST' })

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
    case 'not_checked':
      return source.transport === 'ssh'
        ? 'Add the deploy key below to the repository at your Git host, then check access.'
        : 'The panel hasn’t reached the repository yet. Check access to make sure it can.'
    case 'host_key_changed':
      return `${source.server ?? 'The server'} now shows a different server key. Nothing is fetched until you check the new key with your Git host and accept it.`
    case 'access_failed':
      return source.problem ? `Couldn't reach the repository: ${source.problem}` : "Couldn't reach the repository."
  }
}

/** Where to add the deploy key and the notification address, for the Git hosts people use most. */
export function gitHostSteps(server?: string): { key: string; notify: string } {
  const host = (server ?? '').toLowerCase()
  if (host === 'github.com' || host.endsWith('.github.com')) {
    return {
      key: 'On GitHub: the repository’s Settings → Deploy keys → Add deploy key. Leave “Allow write access” off.',
      notify: 'Settings → Webhooks → Add webhook: paste the address, choose “application/json” and enter the secret.',
    }
  }
  if (host === 'gitlab.com' || host.includes('gitlab')) {
    return {
      key: 'On GitLab: the project’s Settings → Repository → Deploy keys. Leave “Grant write permissions” off.',
      notify: 'Settings → Webhooks: paste the address, enter the secret as “Secret token” and choose “Push events”.',
    }
  }
  if (host === 'bitbucket.org' || host.includes('bitbucket')) {
    return {
      key: 'On Bitbucket: Repository settings → Access keys → Add key.',
      notify: 'Repository settings → Webhooks → Add webhook: paste the address, enter the secret and choose “Repository push”.',
    }
  }
  return {
    key: 'At your Git host (Codeberg, Forgejo, Gitea and most others): the repository’s Settings → Deploy keys. Read access is enough.',
    notify: 'Settings → Webhooks → add one: paste the address, choose the content type “application/json”, enter the secret and choose push events.',
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
      if (run.trigger !== 'pull_request') return { label: 'Published', tone: 'positive' }
      return run.preview_gone ? { label: 'Preview removed', tone: 'neutral' } : { label: 'Preview ready', tone: 'positive' }
    case 'failed':
      return { label: 'Failed', tone: 'warning' }
    case 'cancelled':
      return { label: 'Cancelled', tone: 'neutral' }
    case 'skipped':
      return { label: 'Replaced by a newer build', tone: 'neutral' }
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
