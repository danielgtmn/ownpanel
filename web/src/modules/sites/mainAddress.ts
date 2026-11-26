import {
  createRedirect,
  deleteRedirect,
  errorMessage,
  isSavedButNotApplied,
  updateRedirect,
  type Redirect,
  type RedirectInput,
} from '../../shared/api/index.ts'
import { saveSite } from './api.ts'

export type MainChoice = 'bare' | 'www' | 'none'

export type AddressPair = { bare: string; www: string }

export function addressPairs(domains: string[]): AddressPair[] {
  const seen = new Set<string>()
  const pairs: AddressPair[] = []
  for (const domain of domains) {
    const bare = domain.startsWith('www.') ? domain.slice(4) : domain
    if (!bare.includes('.') || seen.has(bare)) continue
    seen.add(bare)
    pairs.push({ bare, www: `www.${bare}` })
  }
  return pairs
}

export type PairState = {
  pair: AddressPair
  siteDomains: string[]
  inSite: { bare: boolean; www: boolean }
  redirects: { bare: Redirect | null; www: Redirect | null }
}

export function pairState(pair: AddressPair, siteDomains: string[], redirects: Redirect[]): PairState {
  const domainRedirect = (domain: string) =>
    redirects.find((redirect) => redirect.source_path === '' && redirect.source_domain === domain) ?? null
  return {
    pair,
    siteDomains,
    inSite: { bare: siteDomains.includes(pair.bare), www: siteDomains.includes(pair.www) },
    redirects: { bare: domainRedirect(pair.bare), www: domainRedirect(pair.www) },
  }
}

export type CurrentSetup =
  | { kind: 'choice'; choice: MainChoice; main: string; other: string }
  | { kind: 'both' }
  | { kind: 'elsewhere'; main: string; other: string; target: string }

function targetHost(target: string): string | null {
  try {
    return new URL(target).hostname.toLowerCase()
  } catch {
    return null
  }
}

function otherOf(pair: AddressPair, main: string): string {
  return main === pair.bare ? pair.www : pair.bare
}

export function currentSetup(state: PairState): CurrentSetup {
  const { pair, inSite, redirects } = state
  if (inSite.bare && inSite.www) return { kind: 'both' }
  const main = inSite.bare ? pair.bare : pair.www
  const other = otherOf(pair, main)
  const redirect = main === pair.bare ? redirects.www : redirects.bare
  if (!redirect) return { kind: 'choice', choice: 'none', main, other }
  if (targetHost(redirect.target) === main) {
    return { kind: 'choice', choice: main === pair.bare ? 'bare' : 'www', main, other }
  }
  return { kind: 'elsewhere', main, other, target: redirect.target }
}

// The address that stays when the other one is not used at all.
export function mainWithoutRedirect(state: PairState): string {
  return state.inSite.bare ? state.pair.bare : state.pair.www
}

export type Step =
  | { kind: 'delete-redirect'; id: string; domain: string }
  | { kind: 'set-domains'; domains: string[]; added: string[]; removed: string[] }
  | { kind: 'create-redirect'; input: RedirectInput }
  | { kind: 'update-redirect'; id: string; input: RedirectInput }

export function mainRedirect(from: string, to: string): RedirectInput {
  return {
    source_domain: from,
    source_path: '',
    target: `https://${to}`,
    code: 301,
    keep_path: true,
    keep_query: true,
  }
}

const sameTarget = (a: string, b: string) => a.replace(/\/$/, '') === b.replace(/\/$/, '')

function matches(redirect: Redirect, input: RedirectInput): boolean {
  return (
    sameTarget(redirect.target, input.target) &&
    redirect.code === input.code &&
    redirect.keep_path === input.keep_path &&
    redirect.keep_query === input.keep_query
  )
}

// Order matters: a domain that redirects can't be added to a site, and a
// site's domain can't get a domain redirect.
export function planMainAddress(state: PairState, choice: MainChoice): Step[] {
  const { pair, inSite, redirects, siteDomains } = state
  const main = choice === 'none' ? mainWithoutRedirect(state) : choice === 'bare' ? pair.bare : pair.www
  const other = otherOf(pair, main)
  const mainRedirectNow = main === pair.bare ? redirects.bare : redirects.www
  const otherRedirectNow = main === pair.bare ? redirects.www : redirects.bare
  const mainInSite = main === pair.bare ? inSite.bare : inSite.www
  const otherInSite = main === pair.bare ? inSite.www : inSite.bare
  const steps: Step[] = []

  if (mainRedirectNow && !mainInSite) {
    steps.push({ kind: 'delete-redirect', id: mainRedirectNow.id, domain: main })
  }
  if (!mainInSite || otherInSite) {
    const domains = siteDomains.filter((domain) => domain !== other)
    if (!domains.includes(main)) domains.push(main)
    steps.push({
      kind: 'set-domains',
      domains,
      added: mainInSite ? [] : [main],
      removed: otherInSite ? [other] : [],
    })
  }
  if (choice === 'none') {
    if (otherRedirectNow) steps.push({ kind: 'delete-redirect', id: otherRedirectNow.id, domain: other })
    return steps
  }
  const wanted = mainRedirect(other, main)
  if (!otherRedirectNow) steps.push({ kind: 'create-redirect', input: wanted })
  else if (!matches(otherRedirectNow, wanted)) steps.push({ kind: 'update-redirect', id: otherRedirectNow.id, input: wanted })
  return steps
}

function hostOf(target: string): string {
  return targetHost(target) ?? target
}

export function describeStep(step: Step, siteName: string): string {
  switch (step.kind) {
    case 'delete-redirect':
      return `Stop redirecting ${step.domain}`
    case 'set-domains': {
      const parts: string[] = []
      if (step.added.length > 0) parts.push(`add ${step.added.join(', ')} to ${siteName}`)
      if (step.removed.length > 0) parts.push(`remove ${step.removed.join(', ')} from ${siteName}`)
      const text = parts.join(' and ')
      return text.charAt(0).toUpperCase() + text.slice(1)
    }
    case 'create-redirect':
      return `Redirect ${step.input.source_domain} to ${hostOf(step.input.target)}`
    case 'update-redirect':
      return `Change the redirect of ${step.input.source_domain} to lead to ${hostOf(step.input.target)}`
  }
}

export function removedDomains(steps: Step[]): string[] {
  return steps.flatMap((step) => (step.kind === 'set-domains' ? step.removed : []))
}

export function pathRedirectsLost(steps: Step[], pathRedirects: Pick<Redirect, 'source_domain'>[]): number {
  const removed = new Set(removedDomains(steps))
  return pathRedirects.filter((redirect) => removed.has(redirect.source_domain)).length
}

export type PlanResult = {
  done: Step[]
  failed: { step: Step; message: string } | null
  remaining: Step[]
  warnings: string[]
}

async function runStep(site: string, step: Step): Promise<void> {
  switch (step.kind) {
    case 'delete-redirect':
      await deleteRedirect(step.id)
      return
    case 'set-domains':
      await saveSite(site, step.domains)
      return
    case 'create-redirect':
      await createRedirect(step.input)
      return
    case 'update-redirect':
      await updateRedirect(step.id, step.input)
      return
  }
}

export async function runPlan(site: string, steps: Step[]): Promise<PlanResult> {
  const result: PlanResult = { done: [], failed: null, remaining: [], warnings: [] }
  for (let index = 0; index < steps.length; index++) {
    const step = steps[index]
    try {
      await runStep(site, step)
      result.done.push(step)
    } catch (error) {
      if (isSavedButNotApplied(error)) {
        result.done.push(step)
        result.warnings.push(errorMessage(error))
        continue
      }
      result.failed = { step, message: errorMessage(error) }
      result.remaining = steps.slice(index + 1)
      break
    }
  }
  return result
}
