import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ConnectionRow, SitesWithoutConnection } from './GitHubSection.tsx'
import {
  readGitHubReturn,
  repositoriesLabel,
  sitesLabel,
  sitesOf,
  sortRepositories,
  validateOrganization,
  type GitHubConnection,
  type GitHubInstallation,
  type GitHubRepository,
} from './github.ts'
import { RepositoryList } from './RepositoriesDialog.tsx'

const installation: GitHubInstallation = {
  id: 7,
  account: 'acme',
  account_type: 'organization',
  repository_selection: 'selected',
  suspended: false,
  repository_count: 3,
  settings_url: 'https://github.com/organizations/acme/settings/installations/7',
  sites: [],
}

const connection: GitHubConnection = {
  id: '01hzconnection',
  app_id: 99,
  name: 'ownpanel panel.example.com 3f2a',
  slug: 'ownpanel-panel-example-com-3f2a',
  owner: 'acme',
  owner_type: 'organization',
  html_url: 'https://github.com/apps/ownpanel-panel-example-com-3f2a',
  settings_url: 'https://github.com/organizations/acme/settings/apps/ownpanel-panel-example-com-3f2a',
  created_by: 'root',
  created_at: '2026-11-26T10:00:00Z',
  last_checked_at: null,
  last_error: '',
  installations: [installation],
}

const remove = () => Promise.resolve()

describe('returning from GitHub', () => {
  it('reads what GitHub sent back', () => {
    expect(readGitHubReturn(new URLSearchParams('github=created&code=abc&state=s'))).toEqual({
      kind: 'created',
      code: 'abc',
      state: 's',
    })
    expect(readGitHubReturn(new URLSearchParams('github=installed&installation_id=42&state=s'))).toEqual({
      kind: 'installed',
      installationId: 42,
      state: 's',
    })
    expect(readGitHubReturn(new URLSearchParams('github=installed&installation_id=42'))).toEqual({ kind: 'installed-elsewhere' })
    expect(readGitHubReturn(new URLSearchParams('github=requested'))).toEqual({ kind: 'requested' })
  })

  it('treats anything incomplete or unknown as failed', () => {
    for (const raw of ['github=created&code=abc', 'github=created&state=s', 'github=failed', 'github=other']) {
      expect(readGitHubReturn(new URLSearchParams(raw)), raw).toEqual({ kind: 'failed' })
    }
    expect(readGitHubReturn(new URLSearchParams('connect=1'))).toBeUndefined()
  })
})

describe('organisation names', () => {
  it('accepts what GitHub accepts', () => {
    for (const name of ['acme', 'my-company', 'A1', 'a'.repeat(39)]) expect(validateOrganization(name), name).toBeUndefined()
  })
  it('refuses the rest with a sentence', () => {
    for (const name of ['', ' ', '-acme', 'acme-', 'my--co', 'a b', 'acme/site', 'a'.repeat(40)]) {
      expect(validateOrganization(name), name).toMatch(/\.$/)
    }
  })
})

describe('a connection', () => {
  it('says how many repositories the panel may see', () => {
    expect(repositoriesLabel(installation)).toBe('3 chosen repositories')
    expect(repositoriesLabel({ ...installation, repository_count: 1 })).toBe('1 chosen repository')
    expect(repositoriesLabel({ ...installation, repository_selection: 'all', repository_count: 12 })).toBe('All repositories (12)')
    expect(repositoriesLabel({ ...installation, repository_selection: 'all', repository_count: null })).toBe('All repositories')
  })

  it('shows the account, its repositories and a way to remove it', () => {
    const html = renderToStaticMarkup(<ConnectionRow connection={connection} onRemove={remove} />)
    expect(html).toContain('<h3 class="record-title">acme</h3>')
    expect(html).toContain('Organisation')
    expect(html).toContain('3 chosen repositories')
    expect(html).toContain('href="https://github.com/organizations/acme/settings/installations/7"')
    expect(html).toMatch(/<button[^>]*>Remove<span class="visually-hidden"> acme<\/span><\/button>/)
    expect(html).not.toContain('Choose repositories')
    expect(html).not.toMatch(/\b(token|manifest|webhook)\b/i)
  })

  it('asks to choose repositories until that is done', () => {
    const html = renderToStaticMarkup(<ConnectionRow connection={{ ...connection, installations: [] }} onRemove={remove} />)
    expect(html).toContain('Not finished: choose which repositories the panel may see.')
    expect(html).toMatch(/<button[^>]*>Choose repositories<span class="visually-hidden"> of acme<\/span><\/button>/)
  })

  it('shows a problem GitHub reported', () => {
    const html = renderToStaticMarkup(
      <ConnectionRow connection={{ ...connection, last_error: 'GitHub no longer accepts the connection.' }} onRemove={remove} />,
    )
    expect(html).toContain('data-tone="problem">GitHub no longer accepts the connection.</p>')
  })
})

describe('linked sites and repositories', () => {
  const site = { site: 'blog', repository: 'acme/website', repository_id: 2, branch: 'main' }
  const repo = (id: number, name: string): GitHubRepository => ({
    id,
    installation_id: 7,
    owner: 'acme',
    name,
    full_name: `acme/${name}`,
    private: id === 2,
    archived: id === 3,
    default_branch: 'main',
    html_url: `https://github.com/acme/${name}`,
  })

  it('says how many sites an account is used by and offers its repositories', () => {
    const html = renderToStaticMarkup(
      <ConnectionRow
        connection={{ ...connection, installations: [{ ...installation, sites: [site] }] }}
        onRemove={remove}
        onShowRepositories={() => undefined}
      />,
    )
    expect(html).toContain('used by 1 site')
    expect(html).toMatch(/<button[^>]*>Show repositories<span class="visually-hidden"> of acme<\/span><\/button>/)
    expect(sitesLabel(2)).toBe('2 sites')
  })

  it('lists repositories with the linked ones first', () => {
    const list = [repo(1, 'api'), repo(3, 'old'), repo(2, 'website')]
    expect(sortRepositories(list, [site]).map((r) => r.name)).toEqual(['website', 'api', 'old'])
    expect(sitesOf(repo(2, 'renamed'), [site])).toEqual([site])
    const html = renderToStaticMarkup(
      <RepositoryList data={{ repositories: list, truncated: true }} installation={{ ...installation, sites: [site] }} />,
    )
    expect(html).toContain('href="https://github.com/acme/website"')
    expect(html).toContain('Private')
    expect(html).toContain('Archived')
    expect(html).toContain('href="#/sites/blog"')
    expect(html).toContain('Only the first 3 repositories are shown.')
  })

  it('says when GitHub lets the panel see none', () => {
    const html = renderToStaticMarkup(
      <RepositoryList data={{ repositories: [], truncated: false }} installation={installation} />,
    )
    expect(html).toContain('GitHub lets the panel see no repositories of acme right now.')
    expect(html).toContain('Choose some on GitHub')
  })

  it('points to sites whose connection was removed', () => {
    expect(renderToStaticMarkup(<SitesWithoutConnection sites={[]} />)).toBe('')
    const html = renderToStaticMarkup(<SitesWithoutConnection sites={[site]} />)
    expect(html).toContain('This site is linked to a repository whose connection was removed.')
    expect(html).toContain('href="#/sites/blog"')
    expect(html).toContain('acme/website')
  })
})
