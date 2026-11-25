import { describe, expect, it } from 'vitest'
import { healthLine, validateMailHost, type HealthCheck } from './mailHealth.ts'

const check = (id: string, state: string, more: Partial<HealthCheck> = {}): HealthCheck => ({
  id,
  state,
  found: [],
  expected: [],
  detail: '',
  ...more,
})

describe('healthLine', () => {
  it('says in words what each check found', () => {
    expect(healthLine(check('service', 'ok'), 'mail.example.com')).toEqual({
      tone: 'positive',
      badge: 'Fine',
      title: 'The mail service runs.',
    })
    expect(healthLine(check('mail_host_dns', 'ok'), 'mail.example.com').title).toBe(
      'mail.example.com points to this server.',
    )
    expect(healthLine(check('outbound_port', 'error'), 'mail.example.com')).toMatchObject({
      tone: 'warning',
      badge: 'Needs fixing',
      title: "This server can't reach other mail servers.",
    })
  })

  it('tells what to ask the hosting provider when the reverse name is missing', () => {
    const line = healthLine(
      check('ptr', 'warning', { expected: ['203.0.113.5'], found: ['static.5.113.0.203.example.net'] }),
      'mail.example.com',
    )
    expect(line.title).toBe("The server's addresses don't carry the name mail.example.com yet.")
    expect(line.hint).toContain('Ask your hosting provider')
    expect(line.hint).toContain('203.0.113.5 to mail.example.com')
    expect(line.hint).toContain('Now: static.5.113.0.203.example.net.')
  })

  it('explains where the mail server name has to point', () => {
    const line = healthLine(
      check('mail_host_dns', 'error', { expected: ['203.0.113.5'], found: ['198.51.100.7'] }),
      'mail.example.com',
    )
    expect(line.hint).toBe('Create records for mail.example.com with 203.0.113.5. Now it points to 198.51.100.7.')
    expect(healthLine(check('mail_host_dns', 'error'), '').title).toBe('The mail server has no name yet.')
  })

  it('admits when something could not be checked', () => {
    expect(healthLine(check('certificate', 'unknown'), 'mail.example.com')).toEqual({
      tone: 'neutral',
      badge: "Couldn't check",
      title: "Couldn't check the certificate of the mail server.",
    })
  })
})

describe('validateMailHost', () => {
  it('wants a plain name', () => {
    expect(validateMailHost('Mail.Example.com.')).toBeNull()
    expect(validateMailHost('')).toBeNull()
    expect(validateMailHost('203.0.113.5')).toBeTruthy()
    expect(validateMailHost('mail')).toBeTruthy()
    expect(validateMailHost('mail example.com')).toBeTruthy()
  })
})
