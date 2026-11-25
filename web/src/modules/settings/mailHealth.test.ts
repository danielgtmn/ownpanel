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
  it('says where mailbox users sign in, or that they cannot yet', () => {
    expect(healthLine(check('self_service', 'ok'), 'mail.example.com').title).toBe(
      'Mailbox users sign in at mail.example.com.',
    )
    const line = healthLine(check('self_service', 'warning', { detail: 'Choose another name.' }), 'mail.example.com')
    expect(line).toMatchObject({ tone: 'warning', title: "Mailbox users don't have their own sign-in page yet." })
    expect(line.ownDetail).toBeFalsy()
    expect(healthLine(check('self_service', 'unknown'), '').title).toBe("Couldn't check where mailbox users sign in.")
  })

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

  it('tells which record the mail server name needs for bounces and forwarded mail', () => {
    const missing = healthLine(check('mail_host_spf', 'warning', { expected: ['v=spf1 a -all'] }), 'mail.example.com')
    expect(missing.title).toBe('Bounces and forwarded mail from mail.example.com may land in spam.')
    expect(missing.hint).toBe('Add a TXT record for mail.example.com with the value v=spf1 a -all.')
    expect(missing.ownDetail).toBe(true)
    const merge = healthLine(
      check('mail_host_spf', 'warning', { expected: ['v=spf1 a ip4:192.0.2.1 -all'], found: ['v=spf1 ip4:192.0.2.1 -all'] }),
      'mail.example.com',
    )
    expect(merge.hint).toContain('in place of the one there now')
    expect(merge.hint).toContain('Now: v=spf1 ip4:192.0.2.1 -all.')
    const conflict = healthLine(check('mail_host_spf', 'warning', { detail: 'several' }), 'mail.example.com')
    expect(conflict.ownDetail).toBeFalsy()
    expect(healthLine(check('mail_host_spf', 'ok'), 'mail.example.com').tone).toBe('positive')
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

describe('blocklists check', () => {
  it('names the lists that stopped answering and says what to do', () => {
    const line = healthLine(
      check('blocklists', 'warning', { found: ['zen.spamhaus.org'], expected: ['zen.spamhaus.org'] }),
      '',
    )
    expect(line).toMatchObject({ badge: 'Check this', ownDetail: true })
    expect(line.title).toBe("zen.spamhaus.org doesn't answer this server, so mail isn't checked against it.")
    expect(line.hint).toContain('Spamhaus')
    expect(healthLine(check('blocklists', 'ok', { expected: ['zen.spamhaus.org'] }), '').title).toBe(
      'Incoming mail is checked against zen.spamhaus.org.',
    )
    expect(healthLine(check('blocklists', 'ok'), '').title).toMatch(/isn't checked/)
    expect(healthLine(check('blocklists', 'unknown'), '').title).toBe("Couldn't check the lists of known spam senders.")
  })
})
