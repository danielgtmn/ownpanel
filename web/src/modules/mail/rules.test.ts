import { describe, expect, it } from 'vitest'
import { buildFamilies, findFamily } from '../../shared/families/index.ts'
import { parseRoute } from '../../shared/routing/index.ts'
import { mailRows } from './index.ts'
import {
  accessFacts,
  SENDER_POLICIES,
  senderPolicyText,
  aliasTitle,
  appPasswordUse,
  changeSentence,
  organisationDomainsText,
  organisationSentence,
  validateAppPasswordName,
  validateDisplayName,
  validateOrganisationName,
  changesToConfirm,
  clientRows,
  domainState,
  formatBytes,
  limitsChange,
  parseTargets,
  recordName,
  recommendedOpen,
  recordPurpose,
  recordStatus,
  recordsNeedWork,
  sortAliases,
  usageText,
  validateLocalPart,
  validateMailDomain,
} from './rules.ts'
import type { RecordChange } from './types.ts'

const GB = 1024 ** 3

describe('domainState', () => {
  it('says an active domain receives mail', () => {
    expect(domainState({ domain: 'example.com', active: true, problem: '' })).toMatchObject({
      tone: 'positive',
      label: 'Receives mail',
    })
  })

  it('says an inactive domain waits for its mail records', () => {
    const state = domainState({ domain: 'example.com', active: false, problem: 'The MX record points elsewhere.' })
    expect(state.tone).toBe('warning')
    expect(state.label).toBe('Waiting for its mail records')
    expect(state.sentence).toContain("the domain's mail records point to this server")
  })
})

describe('sizes', () => {
  it('formats bytes for people', () => {
    expect(formatBytes(5 * GB)).toBe('5 GB')
    expect(formatBytes(1.5 * GB)).toBe('1.5 GB')
    expect(formatBytes(12 * GB)).toBe('12 GB')
    expect(formatBytes(300 * 1024 ** 2)).toBe('300 MB')
    expect(formatBytes(2048)).toBe('2 KB')
    expect(formatBytes(10)).toBe('10 bytes')
  })

  it('describes usage with and without a quota or usage', () => {
    const usage = { used_bytes: 512 * 1024 ** 2, messages: 3, blocked: false, blocked_reason: '', blocked_at: null }
    expect(usageText({ quota_bytes: 5 * GB, usage })).toBe('512 MB of 5 GB used')
    expect(usageText({ quota_bytes: 0, usage })).toBe('512 MB used, no storage limit')
    expect(usageText({ quota_bytes: 5 * GB, usage: null })).toBe('Room for 5 GB')
  })
})

describe('limitsChange', () => {
  const current = { quota_bytes: 5 * GB, send_per_hour: 100, send_per_day: 500 }

  it('sends only what changed', () => {
    expect(limitsChange(current, { quotaGb: '5', perHour: 100, perDay: 500 }, false).change).toEqual({})
    expect(limitsChange(current, { quotaGb: '2,5', perHour: 50, perDay: 500 }, false).change).toEqual({
      quota_bytes: 2.5 * GB,
      send_per_hour: 50,
    })
  })

  it('lets only admins remove the storage limit', () => {
    expect(limitsChange(current, { quotaGb: '', perHour: 100, perDay: 500 }, true).change).toEqual({ quota_bytes: 0 })
    expect(limitsChange(current, { quotaGb: '', perHour: 100, perDay: 500 }, false).errors.quota).toBeTruthy()
  })

  it('rejects numbers that make no sense', () => {
    const { errors } = limitsChange(current, { quotaGb: 'lots', perHour: 0, perDay: null }, true)
    expect(errors.quota).toBeTruthy()
    expect(errors.perHour).toBeTruthy()
    expect(errors.perDay).toBeTruthy()
  })
})

describe('addresses', () => {
  it('checks the part before the @', () => {
    expect(validateLocalPart('info')).toBeNull()
    expect(validateLocalPart('First.Last+news')).toBeNull()
    expect(validateLocalPart('')).toBeTruthy()
    expect(validateLocalPart('info@example.com')).toBe('Enter only the part before the @.')
    expect(validateLocalPart('two words')).toBeTruthy()
    expect(validateLocalPart('.dot')).toBeTruthy()
  })

  it('checks a domain for e-mail', () => {
    expect(validateMailDomain('Example.com.')).toBeNull()
    expect(validateMailDomain('')).toBeTruthy()
    expect(validateMailDomain('you@example.com')).toBeTruthy()
    expect(validateMailDomain('203.0.113.5')).toBeTruthy()
  })

  it('reads the addresses to forward to', () => {
    expect(parseTargets('A@example.com\nb@example.org, a@example.com')).toEqual({
      targets: ['a@example.com', 'b@example.org'],
      error: null,
    })
    expect(parseTargets('  ').error).toBe('Enter at least one address to deliver to.')
    expect(parseTargets('nobody').error).toBe('nobody is not an e-mail address.')
  })

  it('names the catch-all and lists it last', () => {
    const all = { catch_all: true, address: '*@example.com', domain: 'example.com' }
    const sales = { catch_all: false, address: 'sales@example.com', domain: 'example.com' }
    expect(aliasTitle(all)).toBe('All other addresses @example.com')
    expect(aliasTitle(sales)).toBe('sales@example.com')
    expect(sortAliases([all, sales])).toEqual([sales, all])
  })
})

describe('settings for mail programs', () => {
  it('lists the server, the ports and the words mail programs use', () => {
    const rows = clientRows({
      server: 'mail.example.com',
      imap_port: 993,
      smtp_port: 465,
      smtp_starttls_port: 587,
      user_name_is_address: true,
    })
    expect(rows[0]).toEqual({ label: 'Server (incoming and outgoing)', value: 'mail.example.com', copy: true })
    expect(rows.map((row) => row.value)).toContain('Port 993, SSL/TLS')
    expect(rows.map((row) => row.value)).toContain('Port 465, SSL/TLS, or port 587, STARTTLS')
    expect(rows.map((row) => row.value)).toContain('The full e-mail address')
  })
})

describe('DNS records', () => {
  it('maps the status of a record', () => {
    expect(recordStatus('ok')).toEqual({ tone: 'positive', label: 'Set up' })
    expect(recordStatus('different').tone).toBe('warning')
    expect(recordStatus('something new')).toEqual(recordStatus('unknown'))
  })

  it('names the domain itself @ and counts what is left to do', () => {
    expect(recordName({ name: '' })).toBe('@')
    expect(recordName({ name: '_dmarc' })).toBe('_dmarc')
    expect(recordsNeedWork([{ status: 'ok' }, { status: 'missing' }, { status: 'unknown' }])).toBe(2)
  })

  it('keeps recommended records apart from the ones mail needs', () => {
    const records = [
      { status: 'ok' },
      { status: 'missing', optional: true },
      { status: 'different', optional: true },
      { status: 'ok', optional: true },
    ]
    expect(recordsNeedWork(records)).toBe(0)
    expect(recommendedOpen(records)).toBe(2)
    expect(recordStatus('missing', true)).toEqual({ tone: 'neutral', label: 'Recommended' })
    expect(recordStatus('ok', true).label).toBe('Set up')
    expect(recordStatus('unknown', true).label).toBe("Couldn't check")
    expect(recordPurpose('autoconfig')).toBe('Lets mail programs set themselves up')
    expect(recordPurpose('autodiscover')).toBe('Lets Outlook set itself up')
  })

  it('explains that taking over the mail moves it away from the other server', () => {
    const mx: RecordChange = {
      kind: 'mx',
      type: 'MX',
      name: '',
      action: 'replace',
      old: ['10 mx.other.net'],
      new: ['10 mail.example.com'],
      needs_confirm: true,
      note: '',
    }
    const dkim: RecordChange = { ...mx, kind: 'dkim', type: 'TXT', action: 'create', old: [], needs_confirm: false }
    expect(changeSentence(mx, 'example.com')).toBe(
      'Mail for example.com currently goes to another server (now 10 mx.other.net). After this change it comes here, and the other server gets no more new mail.',
    )
    expect(changesToConfirm([mx, dkim])).toEqual([mx])
  })
})

describe('sidebar rows', () => {
  const families = buildFamilies({
    sites: [{ name: 'blog', domains: ['example.com'], live: true }],
    zones: [],
    redirects: [],
    mailDomains: [
      { domain: 'example.com', active: true },
      { domain: 'shop.example.com', active: false },
    ],
  })
  const family = findFamily(families, 'example.com')!

  it('shows one row per mail domain with its state', () => {
    const rows = mailRows(family)
    expect(rows.map((row) => [row.label, row.hint, row.status])).toEqual([
      ['E-mail', 'example.com', 'live'],
      ['E-mail', 'shop.example.com', 'attention'],
    ])
    expect(rows[1].href).toBe('#/mail/shop.example.com')
    expect(rows[1].isCurrent(parseRoute('#/mail/shop.example.com'))).toBe(true)
    expect(rows[0].isCurrent(parseRoute('#/mail/shop.example.com'))).toBe(false)
  })

  it('needs no hint for the only mail domain of a family', () => {
    const [only] = buildFamilies({ sites: [], zones: [], redirects: [], mailDomains: [{ domain: 'example.org', active: true }] })
    expect(mailRows(only)[0].hint).toBeUndefined()
  })
})

describe('sender names, app passwords and organisations', () => {
  it('accepts what the mail server accepts as a sender name', () => {
    expect(validateDisplayName('')).toBeNull()
    expect(validateDisplayName('  Anna Muster  ')).toBeNull()
    expect(validateDisplayName('a'.repeat(64))).toBeNull()
    expect(validateDisplayName('a'.repeat(65))).toMatch(/64/)
    expect(validateDisplayName('Anna\nMuster')).toBe('Use a single line.')
    expect(validateDisplayName('Anna <a@x>')).toMatch(/Leave out/)
    expect(validateDisplayName('Anna; Max')).toMatch(/Leave out/)
    expect(validateDisplayName('"Anna"')).toMatch(/Leave out/)
  })

  it('needs a name for an app password and an organisation', () => {
    expect(validateAppPasswordName(' ')).toMatch(/Enter/)
    expect(validateAppPasswordName('Phone')).toBeNull()
    expect(validateAppPasswordName('x'.repeat(65))).toMatch(/64/)
    expect(validateOrganisationName('')).toMatch(/Enter/)
    expect(validateOrganisationName('Acme')).toBeNull()
  })

  it('tells when an app password was last used', () => {
    const format = (value: string) => `at ${value}`
    expect(appPasswordUse({ last_used_at: null, last_used_network: '' }, format)).toBe('Not used yet')
    expect(appPasswordUse({ last_used_at: 'T', last_used_network: '203.0.113.0/24' }, format)).toBe(
      'Last used at T from 203.0.113.0/24',
    )
    expect(appPasswordUse({ last_used_at: 'T', last_used_network: '' }, format)).toBe('Last used at T')
  })

  it('describes the organisation of a domain', () => {
    expect(organisationSentence(null)).toBe('This domain stands alone.')
    const organisation = { id: 'o1', name: 'Acme', own: true, domains: ['acme.org', 'acme.net'] }
    expect(organisationSentence(organisation)).toBe('Part of Acme.')
    expect(organisationDomainsText(organisation)).toBe('Together with acme.org, acme.net.')
    expect(organisationDomainsText({ ...organisation, domains: [] })).toBe('No other domains you can see belong to it.')
  })
})

describe('shared mailboxes', () => {
  const person = (address: string) => ({ mailbox_id: '1', address, display_name: '', rights: ['read'], active: true })
  const box = (address: string) => ({ mailbox_id: '2', address, kind: 'shared' as const, rights: ['read'], active: true })

  it('sums up who uses a shared mailbox', () => {
    expect(accessFacts({ kind: 'shared', shared_with: [], access_to: [] })).toEqual(['Nobody has access yet'])
    expect(
      accessFacts({
        kind: 'shared',
        shared_with: [person('a@x.com'), person('b@x.com'), person('c@x.com')],
        access_to: [],
      }),
    ).toEqual(['Used by a@x.com, b@x.com and 1 more'])
  })

  it('sums up what a personal mailbox uses and who it is shared with, and says nothing when there is nothing', () => {
    expect(accessFacts({ kind: 'personal', shared_with: [], access_to: [] })).toEqual([])
    expect(accessFacts({ kind: 'personal', shared_with: [person('ben@x.com')], access_to: [box('info@x.com')] })).toEqual([
      'Has access to info@x.com',
      'Shared with ben@x.com',
    ])
  })

  it('names who may write to an address only when it is limited', () => {
    expect(senderPolicyText('anyone')).toBe('')
    expect(senderPolicyText('organisation')).toBe('Only the organisation may write to it')
    expect(senderPolicyText('members')).toBe('Only its members may write to it')
    expect(SENDER_POLICIES.map((policy) => policy.value)).toEqual(['anyone', 'organisation', 'members'])
  })
})
