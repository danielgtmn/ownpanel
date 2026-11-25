import {
  panelReady,
  type PanelChecks,
  type PanelSettings,
  type SaveResult,
  type SettingsChange,
} from '../../shared/settings/index.ts'
import { normalizeDomain, validateDomain, validateEmail } from '../../shared/settings/rules.ts'

export type StepId = 'account' | 'addresses' | 'panel' | 'done'

export const STEPS: { id: StepId; name: string }[] = [
  { id: 'account', name: 'Account' },
  { id: 'addresses', name: 'Server addresses' },
  { id: 'panel', name: 'Panel address' },
  { id: 'done', name: 'Done' },
]

export function stepNumber(step: StepId): number {
  return STEPS.findIndex((candidate) => candidate.id === step) + 1
}

// The account exists once the wizard shows; saved addresses mean the first wizard step was done.
export function initialStep(settings: PanelSettings): StepId {
  if (settings.server_ips.length === 0 && !settings.server_ips_locked) return 'addresses'
  return 'panel'
}

export function nextStep(step: StepId): StepId {
  const index = stepNumber(step)
  return STEPS[Math.min(index, STEPS.length - 1)].id
}

export function previousStep(step: StepId): StepId {
  const index = stepNumber(step) - 2
  return STEPS[Math.max(index, 1)].id
}

export type ContinueContext = {
  settings: PanelSettings
  selectedAddresses: string[]
}

export function canContinue(step: StepId, context: ContinueContext): boolean {
  switch (step) {
    case 'account':
      return true
    case 'addresses':
      return context.settings.server_ips_locked || context.selectedAddresses.length > 0
    case 'panel':
      // Continue checks and saves what was entered; problems show at the fields.
      return true
    case 'done':
      return false
  }
}

export function continueHint(step: StepId, context: ContinueContext): string | null {
  if (canContinue(step, context)) return null
  if (step === 'addresses') return 'Choose at least one address, or add one.'
  return null
}

export function canSkip(step: StepId, settings: PanelSettings): boolean {
  return step === 'panel' && settings.panel_domain === '' && !settings.panel_domain_locked
}

export const SKIP_NOTE =
  'Without a domain, the panel stays at this IP address with a browser warning. You can add one later under Settings.'

export type PanelDraft = { domain: string; email: string }

export type PanelStepCheck = {
  domainError: string | null
  emailError: string | null
  /** What to save; empty when nothing changed. Only meaningful without errors. */
  change: SettingsChange
}

// Checks the panel step's fields for Continue: a domain is required unless it is locked, the e-mail is optional.
export function checkPanelStep(settings: PanelSettings, draft: PanelDraft): PanelStepCheck {
  const change: SettingsChange = {}
  let domainError: string | null = null
  let emailError: string | null = null
  if (!settings.panel_domain_locked) {
    const domain = normalizeDomain(draft.domain)
    domainError = validateDomain(domain)
    if (!domainError && domain !== settings.panel_domain) change.panel_domain = domain
  }
  if (!settings.acme_email_locked) {
    const email = draft.email.trim()
    emailError = validateEmail(email)
    if (!emailError && email !== settings.acme_email) change.acme_email = email
  }
  return { domainError, emailError, change }
}

export type FinishOutcome = 'app' | 'open' | 'wait'

export function finishOutcome(settings: PanelSettings, checks: PanelChecks | null): FinishOutcome {
  if (!settings.panel_domain) return 'app'
  return checks && panelReady(checks) ? 'open' : 'wait'
}

export function finishQuestion(settings: PanelSettings, checks: PanelChecks | null) {
  const domain = settings.panel_domain
  if (!domain || checks?.dns.state === 'points_here') return null
  return {
    title: 'Finish before the domain is ready?',
    description: `${domain} doesn't point to this server yet. From now on, this IP address sends you to ${domain}, so you will only reach the panel once the domain points to this server. Finish anyway?`,
    confirmLabel: 'Finish anyway',
    cancelLabel: 'Go back',
  }
}

export const REDIRECT_DELAY = 3000

type Question = NonNullable<ReturnType<typeof finishQuestion>>

export type FinishSteps = {
  loadChecks: () => Promise<PanelChecks>
  ask: (question: Question) => Promise<boolean>
  complete: () => Promise<SaveResult>
}

export type FinishResult =
  | { status: 'cancelled' }
  | { status: 'finished'; outcome: FinishOutcome; result: SaveResult }

// Checks the domain afresh, asks before finishing with a domain that doesn't point here yet, then finishes.
export async function runFinish(settings: PanelSettings, steps: FinishSteps): Promise<FinishResult> {
  const checks = settings.panel_domain ? await steps.loadChecks().catch(() => null) : null
  const question = finishQuestion(settings, checks)
  if (question && !(await steps.ask(question))) return { status: 'cancelled' }
  const result = await steps.complete()
  return { status: 'finished', outcome: finishOutcome(result.settings, checks), result }
}
