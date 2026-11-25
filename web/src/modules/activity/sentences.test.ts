import { describe, expect, it } from 'vitest'
import { describeActivity, describeActor, describeDetail, nextLimit } from './sentences.ts'

const entry = (action: string, target = '') => ({ actor: 'anna', action, target })

describe('describeActivity', () => {
  it.each([
    ['site.create', 'blog', 'anna created the site blog'],
    ['site.domains', 'blog', 'anna changed the domains of blog'],
    ['site.publish', 'blog', 'anna published new files for blog'],
    ['site.make-live', 'blog', 'anna made another version of blog live'],
    ['site.delete', 'shop', 'anna removed the site shop'],
    ['user.create', 'max', 'anna added max'],
    ['user.update', 'max', 'anna changed the account of max'],
    ['user.delete', 'max', 'anna removed max'],
    ['token.create', 'deploy', 'anna created the access token deploy'],
    ['token.delete', 'deploy', 'anna deleted the access token deploy'],
    ['account.password', '', 'anna changed their password'],
    ['account.login', '', 'anna signed in'],
    ['setup', '', 'anna set up the panel'],
  ])('describes %s', (action, target, sentence) => {
    expect(describeActivity(entry(action, target))).toBe(sentence)
  })

  it('falls back to a neutral sentence for unknown actions', () => {
    expect(describeActivity(entry('dns.record.add', 'example.com'))).toBe('anna made a change to example.com')
    expect(describeActivity(entry('dns.record.add'))).toBe('anna made a change')
  })

  it('copes with a missing target and a missing actor', () => {
    expect(describeActivity(entry('site.publish'))).toBe('anna published new files for a site')
    expect(describeActivity({ actor: '', action: 'account.login', target: '' })).toBe('Someone signed in')
  })
})

describe('e-mail activity', () => {
  it.each([
    ['settings.update', '', 'anna changed the settings'],
    ['mail.domain.enable', 'example.com', 'anna turned on e-mail for example.com'],
    ['mail.domain.delete', 'example.com', 'anna deleted the e-mail of example.com'],
    ['mail.mailbox.create', 'info@example.com', 'anna created the mailbox info@example.com'],
    ['mail.mailbox.password', 'info@example.com', 'anna changed the password of info@example.com'],
    ['mail.mailbox.limits', 'info@example.com', 'anna changed the limits of info@example.com'],
    ['mail.mailbox.delete', 'info@example.com', 'anna deleted the mailbox info@example.com'],
    ['mail.mailbox.unblock', 'info@example.com', 'anna let info@example.com send again'],
    ['mail.alias.create', 'sales@example.com', 'anna added forwarding for sales@example.com'],
    ['mail.alias.update', '*@example.com', 'anna changed the forwarding of all other addresses @example.com'],
    ['mail.alias.delete', 'sales@example.com', 'anna deleted the forwarding of sales@example.com'],
  ])('describes %s', (action, target, sentence) => {
    expect(describeActivity(entry(action, target))).toBe(sentence)
  })

  it('describes what the system and the mail server did', () => {
    expect(describeActivity({ actor: 'system', action: 'mail.domain.activate', target: 'example.com' })).toBe(
      'example.com started receiving mail here',
    )
    expect(describeActivity({ actor: 'mail', action: 'mail.mailbox.blocked', target: 'info@example.com' })).toBe(
      'The mail server stopped info@example.com from sending',
    )
    expect(describeActivity({ actor: 'mail', action: 'mail.delivery.bounced', target: 'info@example.com' })).toBe(
      'Mail from info@example.com could not be delivered',
    )
    expect(describeActivity({ actor: 'mail', action: 'mail.mailbox.sign-in-failures', target: 'info@example.com' })).toBe(
      'Many failed sign-ins to info@example.com',
    )
  })

  it('turns the details into plain words', () => {
    expect(describeDetail({ action: 'mail.mailbox.create', detail: 'quota 5120 MiB, 100 messages per hour, 500 per day' })).toBe(
      'Storage 5 GB · 100 messages an hour · 500 a day',
    )
    expect(describeDetail({ action: 'mail.mailbox.limits', detail: 'no quota, 50 messages per hour, 200 per day' })).toBe(
      'No storage limit · 50 messages an hour · 200 a day',
    )
    expect(describeDetail({ action: 'mail.domain.enable', detail: 'DKIM selector op202611' })).toBe('')
    expect(describeDetail({ action: 'mail.domain.activate', detail: 'MX points to mail.example.com' })).toBe(
      'Its mail records point to mail.example.com',
    )
    expect(describeDetail({ action: 'mail.domain.delete', detail: '2 mailboxes, 1 aliases' })).toBe(
      '2 mailboxes, 1 forwarding address',
    )
    expect(describeDetail({ action: 'mail.alias.update', detail: 'was a@example.com; to b@example.org' })).toBe(
      'Was a@example.com · To b@example.org',
    )
  })
})

describe('nextLimit', () => {
  it('steps from 50 to 100 to 200 and then stops', () => {
    expect(nextLimit(50)).toBe(100)
    expect(nextLimit(100)).toBe(200)
    expect(nextLimit(200)).toBeNull()
  })
})

describe('describeActor', () => {
  it('names the access token in plain words', () => {
    expect(describeActor('admin via token deploy script')).toBe('admin (with the access token “deploy script”)')
    expect(describeActor('anna')).toBe('anna')
    expect(describeActor('')).toBe('Someone')
  })
})

describe('describeDetail', () => {
  it.each([
    ['site.publish', '20261124T201315.622Z-fc3af4', /^Version from /],
    ['site.publish', 'something else', /^something else$/],
    ['site.create', 'a.example.com, b.example.com', /^Domains: a\.example\.com, b\.example\.com$/],
    ['user.create', 'role member', /^Role: Member$/],
    ['user.update', 'role admin, password changed', /^Role: Administrator · New password$/],
    ['token.create', 'write access, sites blog, shop', /^Read and change · Only blog, shop$/],
    ['token.create', 'read access, all sites', /^Read only · All sites$/],
    ['setup', 'first admin created', /^$/],
    ['update.request', 'v1.1.0 to v1.2.0 (stable)', /^From v1\.1\.0 to v1\.2\.0 · Stable releases$/],
    ['update.request', 'nightly-20261124-878136d to v1.2.0 (stable), an older version', / · Older version$/],
    ['update.auto', 'off', /^Off$/],
    ['update.auto', 'stable releases between 03:00 and 05:00', /^On: stable releases between 03:00 and 05:00$/],
    ['update.result', 'updated from v1.1.0 to v1.2.0', /^Updated from v1\.1\.0 to v1\.2\.0$/],
    ['unknown.action', 'raw', /^raw$/],
  ])('%s %s', (action, detail, want) => {
    expect(describeDetail({ action, detail })).toMatch(want)
  })
})
