import { request } from '../../shared/api/index.ts'

export type GitHubInstallation = {
  id: number
  account: string
  account_type: 'user' | 'organization'
  repository_selection: 'all' | 'selected'
  suspended: boolean
  repository_count: number | null
  settings_url: string
}

export type GitHubConnection = {
  id: number
  app_id: number
  name: string
  slug: string
  owner: string
  owner_type: 'user' | 'organization'
  html_url: string
  settings_url: string
  created_by: string
  created_at: string
  last_checked_at: string | null
  last_error: string
  installations: GitHubInstallation[]
}

export type GitHubState = {
  ready: boolean
  problem?: string
  apps: GitHubConnection[]
}

export type GitHubRemoved = { name: string; settings_url: string }

export const getGitHub = () => request<GitHubState>('/github')

export const refreshGitHub = () => request<GitHubState>('/github/refresh', { method: 'POST' })

/** Starts connecting; the browser then goes to the returned address (the panel's page that continues to GitHub). */
export const startConnecting = (organization: string) =>
  request<{ url: string }>('/github/apps', { method: 'POST', body: { organization } })

export const finishConnecting = (code: string, state: string) =>
  request<GitHubConnection>('/github/apps/finish', { method: 'POST', body: { code, state } })

/** Returns GitHub's page where the repositories are chosen. */
export const startChoosingRepositories = (id: number) =>
  request<{ url: string }>(`/github/apps/${id}/install`, { method: 'POST' })

export const finishChoosingRepositories = (installationId: number, state: string) =>
  request<GitHubConnection>('/github/installations', {
    method: 'POST',
    body: { installation_id: installationId, state },
  })

export const removeConnection = (id: number) => request<GitHubRemoved>(`/github/apps/${id}`, { method: 'DELETE' })

/** What the address says when GitHub sent the person back to the settings. */
export type GitHubReturn =
  | { kind: 'created'; code: string; state: string }
  | { kind: 'installed'; installationId: number; state: string }
  | { kind: 'installed-elsewhere' }
  | { kind: 'requested' }
  | { kind: 'failed' }

export function readGitHubReturn(query: URLSearchParams): GitHubReturn | undefined {
  const step = query.get('github')
  if (!step) return undefined
  const state = query.get('state') ?? ''
  switch (step) {
    case 'created': {
      const code = query.get('code') ?? ''
      return code && state ? { kind: 'created', code, state } : { kind: 'failed' }
    }
    case 'installed': {
      const id = Number(query.get('installation_id'))
      if (Number.isSafeInteger(id) && id > 0 && state) return { kind: 'installed', installationId: id, state }
      // Changed on GitHub without coming from here: just look again.
      return { kind: 'installed-elsewhere' }
    }
    case 'requested':
      return { kind: 'requested' }
    default:
      return { kind: 'failed' }
  }
}

export function accountLabel(type: 'user' | 'organization'): string {
  return type === 'organization' ? 'Organisation' : 'Personal account'
}

export function repositoriesLabel(installation: GitHubInstallation): string {
  const count = installation.repository_count
  if (installation.repository_selection === 'all') {
    return count === null ? 'All repositories' : `All repositories (${count})`
  }
  if (count === null) return 'Chosen repositories'
  return count === 1 ? '1 chosen repository' : `${count} chosen repositories`
}

export function totalRepositories(connection: GitHubConnection): number | null {
  let total = 0
  for (const installation of connection.installations) {
    if (installation.repository_count === null) return null
    total += installation.repository_count
  }
  return total
}

const ORGANIZATION = /^[A-Za-z0-9](?:[A-Za-z0-9]|-(?=[A-Za-z0-9])){0,38}$/

/** Checks an organisation name the way GitHub allows it; returns a message or undefined. */
export function validateOrganization(name: string): string | undefined {
  const value = name.trim()
  if (!value) return 'Enter the name of the organisation as it appears on GitHub.'
  if (!ORGANIZATION.test(value)) {
    return 'Use the name from the organisation’s address on GitHub: letters, digits and single hyphens.'
  }
  return undefined
}
