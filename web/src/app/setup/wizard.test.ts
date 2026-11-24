import { afterEach, describe, expect, it, vi } from 'vitest'
import { finishWizard, getPanelChecks } from '../../shared/settings/index.ts'
import { checksFixture, json, mockFetch, settingsFixture } from '../../shared/settings/testing.ts'
import {
  canContinue,
  canSkip,
  continueHint,
  finishOutcome,
  finishQuestion,
  initialStep,
  nextStep,
  previousStep,
  runFinish,
  stepLabel,
} from './wizard.ts'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('steps', () => {
  it('counts four steps', () => {
    expect(stepLabel('addresses')).toBe('Step 2 of 4')
    expect(stepLabel('done')).toBe('Step 4 of 4')
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
  const base = { settings: settingsFixture(), selectedAddresses: [] as string[], domainDirty: false }

  it('needs at least one address, unless they are locked', () => {
    expect(canContinue('addresses', base)).toBe(false)
    expect(continueHint('addresses', base)).toMatch(/at least one/)
    expect(canContinue('addresses', { ...base, selectedAddresses: ['203.0.113.5'] })).toBe(true)
    expect(
      canContinue('addresses', { ...base, settings: settingsFixture({ server_ips: ['1.2.3.4'], server_ips_locked: true }) }),
    ).toBe(true)
  })

  it('needs a saved domain without unsaved edits', () => {
    expect(canContinue('panel', base)).toBe(false)
    expect(continueHint('panel', base)).toMatch(/skip/)
    const saved = { ...base, settings: settingsFixture({ panel_domain: 'panel.example.com' }) }
    expect(canContinue('panel', saved)).toBe(true)
    expect(continueHint('panel', saved)).toBeNull()
    expect(canContinue('panel', { ...saved, domainDirty: true })).toBe(false)
    expect(continueHint('panel', { ...saved, domainDirty: true })).toMatch(/save the domain/i)
  })

  it('has nothing to continue to after the last step', () => {
    expect(canContinue('done', base)).toBe(false)
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
