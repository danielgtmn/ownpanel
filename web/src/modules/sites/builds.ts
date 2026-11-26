import { request } from '../../shared/api/index.ts'
import type { Run } from './source.ts'

/** A variable of the build as shown: secret values are never sent back. */
export type BuildVariable = { name: string; value?: string; secret: boolean }

export type BuildSettings = {
  site: string
  /** False until settings were saved; builds ask for them until then. */
  configured: boolean
  command: string
  output: string
  image: string
  variables: BuildVariable[]
  updated_by?: string
  updated_at?: string
  /** Build environments this server offers. */
  images: string[]
  /** False when this server does not build sites. */
  builds_available: boolean
}

/** A variable to save. Leaving out the value keeps the one stored for that name. */
export type BuildVariableInput = { name: string; value?: string; secret: boolean }

export type BuildSettingsInput = {
  command: string
  output: string
  image: string
  variables: BuildVariableInput[]
}

export type Confidence = 'high' | 'medium' | 'low' | 'none'

/** What the repository's files suggest. */
export type BuildProposal = {
  site: string
  branch: string
  commit: string
  preset: string
  name: string
  confidence: Confidence
  buildable: boolean
  settings: { command: string; output: string; image?: string }
  package_manager?: string
  also?: string[]
  notes?: string[]
}

/** A part of a run's log. text is what the build printed: show it as text only. */
export type RunLog = {
  run_id: string
  status: Run['status']
  text: string
  offset: number
  next: number
  done: boolean
  truncated: boolean
  live: boolean
}

const sitePath = (site: string) => `/sites/${encodeURIComponent(site)}`

export const getBuildSettings = (site: string) => request<BuildSettings>(`${sitePath(site)}/build-settings`)

export const saveBuildSettings = (site: string, input: BuildSettingsInput) =>
  request<BuildSettings>(`${sitePath(site)}/build-settings`, { method: 'PUT', body: input })

export const proposeBuildSettings = (site: string) =>
  request<BuildProposal>(`${sitePath(site)}/build-settings/proposal`)

export const startBuild = (site: string) => request<Run>(`${sitePath(site)}/runs`, { method: 'POST' })

export const cancelRun = (site: string, run: string) =>
  request<Run>(`${sitePath(site)}/runs/${encodeURIComponent(run)}/cancel`, { method: 'POST' })

export const getRunLog = (site: string, run: string, offset: number) =>
  request<RunLog>(`${sitePath(site)}/runs/${encodeURIComponent(run)}/log?offset=${offset}`)

/** How often a running build's log is asked for. */
export const LOG_REFRESH_MS = 1500
/** How often the list of builds is refreshed while one waits or runs. */
export const ACTIVE_REFRESH_MS = 3000

/** "node-24" reads "Node 24"; anything else stays as it is. */
export function environmentLabel(image: string): string {
  const match = /^node-(\d+)$/.exec(image)
  return match ? `Node ${match[1]}` : image
}

/** How sure the suggestion is, for people. */
export function confidenceLabel(confidence: Confidence): string {
  switch (confidence) {
    case 'high':
      return 'Recognised'
    case 'medium':
      return 'Probably'
    case 'low':
      return 'A guess'
    case 'none':
      return 'Not recognised'
  }
}

/** One line about the saved settings, for the Builds section. */
export function settingsSummary(settings: BuildSettings): string {
  if (!settings.configured) return 'Not set up yet: choose how the site is built before the first build.'
  const steps = settings.command.split('\n').filter((line) => line.trim() !== '').length
  const what = steps === 0 ? 'Publishes the folder as it is' : `Runs ${steps === 1 ? '1 command' : `${steps} commands`}`
  const folder = settings.output === '.' ? 'the whole repository' : `the folder ${settings.output}`
  const vars = settings.variables.length
  const env = settings.image ? ` with ${environmentLabel(settings.image)}` : ''
  return `${what}${steps === 0 ? '' : env}, publishes ${folder}${vars ? `, ${vars === 1 ? '1 variable' : `${vars} variables`}` : ''}.`
}

/** Variable names the build accepts: upper-case letters, digits and _, not starting with a digit. */
export function validVariableName(name: string): boolean {
  return /^[A-Z_][A-Z0-9_]*$/.test(name) && name.length <= 128
}

export type VariableRow = {
  key: number
  name: string
  value: string
  secret: boolean
  /** Stored before: an empty value keeps the stored one. */
  stored: boolean
}

/** Checks the rows of the variables; returns a message for people or null. */
export function checkVariables(rows: VariableRow[]): string | null {
  const seen = new Set<string>()
  for (const row of rows) {
    const name = row.name.trim()
    if (!name) return 'Every variable needs a name.'
    if (!validVariableName(name)) return `${name}: use upper-case letters, digits and _ only, not starting with a digit.`
    if (seen.has(name)) return `${name} is there twice.`
    seen.add(name)
    if (!row.stored && row.value === '') return `Enter a value for ${name}.`
    if (row.secret && row.value !== '' && row.value.length < 4) return `${name}: a secret value needs at least 4 characters.`
  }
  return null
}

/** The rows as saved: a stored variable whose value was left empty keeps its value. */
export function variablesInput(rows: VariableRow[]): BuildVariableInput[] {
  return rows.map((row) => {
    const out: BuildVariableInput = { name: row.name.trim(), secret: row.secret }
    if (!(row.stored && row.value === '')) out.value = row.value
    return out
  })
}

/** Rows to edit from saved settings: secret values stay empty (unchanged unless typed). */
export function variableRows(settings: BuildSettings): VariableRow[] {
  return settings.variables.map((v, key) => ({ key, name: v.name, value: v.value ?? '', secret: v.secret, stored: true }))
}

/** The run to open from the address (?run=…), as GitHub links to it. */
export function runFromQuery(query: URLSearchParams): string | null {
  const id = query.get('run')
  return id && /^[A-Za-z0-9_-]{1,64}$/.test(id) ? id : null
}
