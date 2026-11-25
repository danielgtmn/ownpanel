import { afterEach, describe, expect, it, vi } from 'vitest'
import { finishWizard, getPanelChecks } from '../../shared/settings/index.ts'
import { checksFixture, json, mockFetch, settingsFixture } from '../../shared/settings/testing.ts'
import {
  canContinue,
  canSkip,
  checkPanelStep,
  continueHint,
  finishOutcome,
  finishQuestion,
  initialStep,
  nextStep,
  previousStep,
  runFinish,
  stepNumber,
} from './wizard.ts'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('steps', () => {
  it('counts four steps', () => {
    expect(stepNumber('addresses')).toBe(2)
    expect(stepNumber('done')).toBe(4)
  })

  it('moves forward and back, never back to the finished account step', () => {
    expect(nextStep('addresses')).toBe('panel')
    expect(nextStep('panel')).toBe('done')
    expect(nextStep('done')).toBe('done')
    expect(previousStep('done')).toBe('panel')
    expect(previousStep('panel')).toBe('addresses')
    expect(previousStep('addresses')).toBe('addresses')
  })

  it('resumes from the saved settings after a reload', () => {
    expect(initialStep(settingsFixture())).toBe('addresses')
    expect(initialStep(settingsFixture({ server_ips: ['203.0.113.5'] }))).toBe('panel')
    expect(initialStep(settingsFixture({ server_ips: ['203.0.113.5'], server_ips_locked: true }))).toBe('panel')
  })
})

describe('can continue', () => {
  const base = { settings: settingsFixture(), selectedAddresses: [] as string[] }

  it('needs at least one address, unless they are locked', () => {
    expect(canContinue('addresses', base)).toBe(false)
    expect(continueHint('addresses', base)).toMatch(/at least one/)
    expect(canContinue('addresses', { ...base, selectedAddresses: ['203.0.113.5'] })).toBe(true)
    expect(
      canContinue('addresses', { ...base, settings: settingsFixture({ server_ips: ['1.2.3.4'], server_ips_locked: true }) }),
    ).toBe(true)
  })

  it('lets Continue check the panel step itself', () => {
    expect(canContinue('panel', base)).toBe(true)
    expect(continueHint('panel', base)).toBeNull()
  })

  it('has nothing to continue to after the last step', () => {
    expect(canContinue('done', base)).toBe(false)
  })
})

describe('the panel step', () => {
  const draft = (domain: string, email = '') => ({ domain, email })

  it('needs a domain', () => {
    const check = checkPanelStep(settingsFixture(), draft(''))
    expect(check.domainError).toMatch(/enter a domain/i)
    expect(check.emailError).toBeNull()
  })

  it('rejects an IP address and a wrong e-mail', () => {
    const check = checkPanelStep(settingsFixture(), draft('203.0.113.5', 'nope'))
    expect(check.domainError).toMatch(/not an IP address/)
    expect(check.emailError).toMatch(/e-mail address/)
  })

  it('saves the cleaned-up domain and the optional e-mail in one go', () => {
    expect(checkPanelStep(settingsFixture(), draft(' https://Panel.Example.com/ ', ' me@example.com '))).toEqual({
      domainError: null,
      emailError: null,
      change: { panel_domain: 'panel.example.com', acme_email: 'me@example.com' },
    })
    expect(checkPanelStep(settingsFixture(), draft('panel.example.com')).change).toEqual({
      panel_domain: 'panel.example.com',
    })
  })

  it('saves nothing when nothing changed', () => {
    const saved = settingsFixture({ panel_domain: 'panel.example.com', acme_email: 'me@example.com' })
    expect(checkPanelStep(saved, draft('panel.example.com', 'me@example.com')).change).toEqual({})
  })

  it('accepts locked fields as they are', () => {
    const locked = settingsFixture({ panel_domain_locked: true, acme_email_locked: true, acme_email: 'x' })
    expect(checkPanelStep(locked, draft('', 'not an e-mail'))).toEqual({ domainError: null, emailError: null, change: {} })
  })
})

describe('skipping the panel address', () => {
  it('is offered only while no domain is set', () => {
    expect(canSkip('panel', settingsFixture())).toBe(true)
    expect(canSkip('panel', settingsFixture({ panel_domain: 'panel.example.com' }))).toBe(false)
    expect(canSkip('panel', settingsFixture({ panel_domain_locked: true }))).toBe(false)
    expect(canSkip('addresses', settingsFixture())).toBe(false)
  })

  it('finishes into the app without asking', async () => {
    const fetchMock = mockFetch(json(settingsFixture({ setup_complete: true })))
    const ask = vi.fn(async () => true)
    const done = await runFinish(settingsFixture(), { loadChecks: getPanelChecks, ask, complete: finishWizard })
    expect(ask).not.toHaveBeenCalled()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/settings/complete')
    expect(done).toMatchObject({ status: 'finished', outcome: 'app' })
  })
})

describe('finishing', () => {
  const withDomain = settingsFixture({ panel_domain: 'panel.example.com', server_ips: ['203.0.113.5'] })
  const finished = settingsFixture({ panel_domain: 'panel.example.com', server_ips: ['203.0.113.5'], setup_complete: true })

  it('opens the domain when DNS and certificate are ready', () => {
    expect(finishOutcome(withDomain, checksFixture('points_here', 'ready'))).toBe('open')
    expect(finishOutcome(withDomain, checksFixture('points_here', 'pending'))).toBe('wait')
    expect(finishOutcome(withDomain, null)).toBe('wait')
    expect(finishOutcome(settingsFixture(), null)).toBe('app')
  })

  it('asks before finishing with a domain that does not point here', () => {
    expect(finishQuestion(withDomain, checksFixture('no_record', 'pending'))?.description).toMatch(
      /you will only reach the panel once the domain points to this server\. Finish anyway\?$/,
    )
    expect(finishQuestion(withDomain, null)).not.toBeNull()
    expect(finishQuestion(withDomain, checksFixture('points_here', 'pending'))).toBeNull()
    expect(finishQuestion(settingsFixture(), null)).toBeNull()
  })

  it('stays in the wizard when the question is declined', async () => {
    const fetchMock = mockFetch(json(checksFixture('points_elsewhere', 'pending', ['198.51.100.7'])))
    const ask = vi.fn(async () => false)
    const done = await runFinish(withDomain, { loadChecks: getPanelChecks, ask, complete: finishWizard })
    expect(ask).toHaveBeenCalledOnce()
    expect(done).toEqual({ status: 'cancelled' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('/api/settings/checks')
  })

  it('finishes when confirmed and waits for the domain', async () => {
    const fetchMock = mockFetch(json(checksFixture('no_record', 'pending')), json(finished))
    const ask = vi.fn(async () => true)
    const done = await runFinish(withDomain, { loadChecks: getPanelChecks, ask, complete: finishWizard })
    expect(fetchMock.mock.calls[1][0]).toBe('/api/settings/complete')
    expect(done).toMatchObject({ status: 'finished', outcome: 'wait', result: { settings: finished, applied: true } })
  })

  it('finishes without asking when everything is ready', async () => {
    mockFetch(json(checksFixture('points_here', 'ready')), json(finished))
    const ask = vi.fn(async () => true)
    const done = await runFinish(withDomain, { loadChecks: getPanelChecks, ask, complete: finishWizard })
    expect(ask).not.toHaveBeenCalled()
    expect(done).toMatchObject({ status: 'finished', outcome: 'open' })
  })

  it('asks when the check itself fails', async () => {
    mockFetch(new TypeError('Failed to fetch'), json(finished))
    const ask = vi.fn(async () => true)
    const done = await runFinish(withDomain, { loadChecks: getPanelChecks, ask, complete: finishWizard })
    expect(ask).toHaveBeenCalledOnce()
    expect(done).toMatchObject({ status: 'finished', outcome: 'wait' })
  })
})
