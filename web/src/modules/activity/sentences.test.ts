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
    ['account.email', '', 'anna changed their e-mail address'],
    ['account.password_reset', '', 'Someone chose a new password with a link sent by e-mail'],
    ['account.password_reset_failed', '', 'A link to choose a new password could not be sent'],
    ['setup', '', 'anna set up the panel'],
    ['setup.complete', '', 'anna finished setting up the panel'],
    ['settings.update', '', 'anna changed the settings'],
    ['site.upload-link', 'blog', 'anna created an upload link for blog'],
    ['redirect.create', 'old.example.com', 'anna added a redirect from old.example.com'],
    ['redirect.update', 'old.example.com/a', 'anna changed the redirect from old.example.com/a'],
    ['redirect.delete', 'old.example.com', 'anna removed the redirect from old.example.com'],
    ['dns.connect', 'Hetzner', 'anna connected Hetzner'],
    ['dns.disconnect', 'Hetzner', 'anna disconnected Hetzner'],
    ['dns.zone.create', 'example.com', 'anna added the domain example.com'],
    ['dns.zone.delete', 'example.com', 'anna removed the domain example.com'],
    ['dns.record.set', 'example.com', 'anna changed a DNS record of example.com'],
    ['dns.record.delete', 'example.com', 'anna deleted a DNS record of example.com'],
    ['dns.point', 'blog.example.com', 'anna pointed blog.example.com to this server'],
  ])('describes %s', (action, target, sentence) => {
    expect(describeActivity(entry(action, target))).toBe(sentence)
  })

  it('falls back to a neutral sentence for unknown actions', () => {
    expect(describeActivity(entry('something.new', 'example.com'))).toBe('anna made a change to example.com')
    expect(describeActivity(entry('something.new'))).toBe('anna made a change')
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
    expect(describeActivity({ actor: 'mail', action: 'mail.mailbox.send-limit', target: 'info@example.com' })).toBe(
      'info@example.com reached its sending limit',
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
    ['user.create', 'role member, with e-mail address', /^Role: Member · With e-mail address$/],
    ['user.update', 'e-mail address removed', /^E-mail address removed$/],
    ['account.email', 'e-mail address changed', /^New e-mail address$/],
    ['token.create', 'write access, sites blog, shop', /^Read and change · Only blog, shop$/],
    ['token.create', 'read access, all sites', /^Read only · All sites$/],
    ['setup', 'first admin created', /^$/],
    ['update.request', 'v1.1.0 to v1.2.0 (stable)', /^From v1\.1\.0 to v1\.2\.0 · Stable releases$/],
    ['update.request', 'nightly-20261124-878136d to v1.2.0 (stable), an older version', / · Older version$/],
    ['update.auto', 'off', /^Off$/],
    ['update.auto', 'stable releases between 03:00 and 05:00', /^On: stable releases between 03:00 and 05:00$/],
    ['update.result', 'updated from v1.1.0 to v1.2.0', /^Updated from v1\.1\.0 to v1\.2\.0$/],
    [
      'mail.delivery.not-forwarded',
      'a message to sales@example.com was not forwarded to a@example.net, b@example.net: it was judged as junk (score 6.5)',
      /^To a@example\.net, b@example\.net · It looked like spam$/,
    ],
    [
      'mail.delivery.not-forwarded',
      'a message to sales@example.com was not forwarded to a@example.net: the domain example.com reached its limit of 300 messages per hour',
      /^To a@example\.net · example\.com reached its limit of 300 messages an hour$/,
    ],
    [
      'mail.delivery.not-forwarded',
      'a delivery report for x@example.com was not passed on: it was judged as junk (score 9)',
      /^Delivery report · It looked like spam$/,
    ],
    ['mail.blocklist.refused', 'the blocklist zen.spamhaus.org refused to answer', /^Incoming mail is not checked/],
    ['unknown.action', 'raw', /^raw$/],
  ])('%s %s', (action, detail, want) => {
    expect(describeDetail({ action, detail })).toMatch(want)
  })
})

describe('mail service sentences', () => {
  it('describes lists that stop answering and mail that was not forwarded', () => {
    expect(describeActivity({ actor: 'mail', action: 'mail.blocklist.refused', target: 'zen.spamhaus.org' })).toBe(
      'The list of spam senders zen.spamhaus.org stopped answering this server',
    )
    expect(describeActivity({ actor: 'mail', action: 'mail.delivery.not-forwarded', target: '*@example.com' })).toBe(
      'Mail to all other addresses @example.com was not forwarded',
    )
    expect(describeActivity({ actor: 'mail', action: 'mail.delivery.not-forwarded', target: '' })).toBe(
      'Mail was not forwarded',
    )
  })
})

describe('organisations, display names and app passwords', () => {
  it.each([
    ['mail.organisation.create', 'Acme', 'anna created the organisation Acme'],
    ['mail.organisation.rename', 'Acme Ltd', 'anna renamed an organisation to Acme Ltd'],
    ['mail.organisation.delete', 'Acme', 'anna deleted the organisation Acme'],
    ['mail.domain.organisation', 'example.com', 'anna changed the organisation of example.com'],
    ['mail.mailbox.display-name', 'info@example.com', 'anna changed the display name of info@example.com'],
    ['mail.mailbox.app-passwords-only', 'info@example.com', 'anna changed how mail programs sign in to info@example.com'],
    ['mail.mailbox.app-password-create', 'info@example.com', 'anna created an app password for info@example.com'],
    ['mail.mailbox.app-password-revoke', 'info@example.com', 'anna revoked an app password of info@example.com'],
  ])('describes %s', (action, target, sentence) => {
    expect(describeActivity(entry(action, target))).toBe(sentence)
  })

  it('describes what a mailbox changed itself', () => {
    const self = (action: string) =>
      describeActivity({ actor: 'info@example.com', action, target: 'info@example.com' })
    expect(self('mail.mailbox.password-changed')).toBe('info@example.com changed its password')
    expect(self('mail.mailbox.display-name-changed')).toBe('info@example.com changed its display name')
    expect(self('mail.mailbox.app-passwords-only')).toBe('info@example.com changed how mail programs sign in to it')
    expect(self('mail.mailbox.app-password-created')).toBe('info@example.com created an app password')
    expect(self('mail.mailbox.app-password-revoked')).toBe('info@example.com revoked an app password')
    expect(describeActivity({ actor: 'mail', action: 'mail.mailbox.app-password-revoked', target: '' })).toBe(
      'An app password was revoked',
    )
  })

  it.each([
    ['mail.organisation.create', '', ''],
    ['mail.organisation.rename', 'was Acme', 'Was Acme'],
    ['mail.organisation.delete', 'its domains now stand alone: a.org, b.org', 'Its domains now stand alone: a.org, b.org'],
    ['mail.organisation.delete', '', ''],
    ['mail.domain.organisation', 'stands alone', 'Stands alone'],
    ['mail.domain.organisation', 'into Acme', 'Now part of Acme'],
    ['mail.domain.organisation', 'into Acme with a.org, b.org', 'Now part of Acme · Together with a.org, b.org'],
    [
      'mail.domain.organisation',
      'into Acme with a.org (domains of different owners)',
      'Now part of Acme · Together with a.org · Domains of different owners',
    ],
    ['mail.domain.organisation', 'into Acme (domains of different owners)', 'Now part of Acme · Domains of different owners'],
    ['mail.mailbox.display-name', 'now Anna Muster', 'Now Anna Muster'],
    ['mail.mailbox.display-name', 'removed', 'Removed'],
    ['mail.mailbox.display-name-changed', 'now Anna', 'Now Anna'],
    [
      'mail.mailbox.display-name-changed',
      'now Anna (not taken: changed in the panel at the same time)',
      'Now Anna · Not taken: it was changed in the panel at the same time',
    ],
    ['mail.mailbox.app-passwords-only', 'on', 'Only app passwords work in mail programs'],
    ['mail.mailbox.app-passwords-only', 'off', 'The mailbox password works in mail programs again'],
    [
      'mail.mailbox.app-passwords-only',
      'on (not taken: changed in the panel at the same time)',
      'Only app passwords work in mail programs · Not taken: it was changed in the panel at the same time',
    ],
    ['mail.mailbox.app-password-create', 'Phone', 'Phone'],
    ['mail.mailbox.app-password-revoke', 'Laptop', 'Laptop'],
    ['mail.mailbox.app-password-created', 'Phone', 'Phone'],
    [
      'mail.mailbox.app-password-revoked',
      'Phone (not taken: changed in the panel at the same time)',
      'Phone · Not taken: it was changed in the panel at the same time',
    ],
    [
      'mail.mailbox.password-changed',
      '(not taken: changed in the panel at the same time)',
      'Not taken: it was changed in the panel at the same time',
    ],
  ])('%s %s', (action, detail, want) => {
    expect(describeDetail({ action, detail })).toBe(want)
  })
})

describe('moving mail in and out', () => {
  it.each([
    ['mail.mailbox.import-started', 'anna started importing mail into info@example.com'],
    ['mail.mailbox.import-cancelled', 'anna cancelled an import into info@example.com'],
    ['mail.mailbox.exported', 'anna downloaded all mail of info@example.com'],
  ])('describes %s', (action, sentence) => {
    expect(describeActivity({ actor: 'anna', action, target: 'info@example.com' })).toBe(sentence)
  })

  it('describes how imports ended', () => {
    const say = (action: string, target = 'info@example.com') => describeActivity({ actor: 'mail', action, target })
    expect(say('mail.mailbox.import-finished')).toBe('Importing mail into info@example.com finished')
    expect(say('mail.mailbox.import-failed')).toBe('Importing mail into info@example.com failed')
    expect(say('mail.mailbox.import-paused')).toBe(
      'Importing mail into info@example.com paused until the other server allows more downloads',
    )
    expect(say('mail.mailbox.import-interrupted')).toBe('Importing mail into info@example.com was interrupted by a restart')
    expect(say('mail.mailbox.import-finished', '')).toBe('Importing mail finished')
  })

  it('names the source and the counts, never more', () => {
    expect(describeDetail({ action: 'mail.mailbox.import-started', detail: 'import 3 from me@old.example at imap.old.example' })).toBe(
      'From me@old.example on imap.old.example',
    )
    expect(
      describeDetail({ action: 'mail.mailbox.import-started', detail: 'import 3 from me@old.example at imap.old.example (continued)' }),
    ).toBe('From me@old.example on imap.old.example · Continued')
    expect(describeDetail({ action: 'mail.mailbox.import-started', detail: 'import 4 from file Takeout.zip' })).toBe(
      'From the file Takeout.zip',
    )
    expect(
      describeDetail({ action: 'mail.mailbox.import-finished', detail: 'import 4 from old.mbox: 12 copied, 3 already there, 0 failed' }),
    ).toBe('From old.mbox · 12 copied · 3 already there')
    expect(
      describeDetail({
        action: 'mail.mailbox.import-failed',
        detail: 'import 5 from me at imap.old.example: 0 copied, 0 already there, 2 failed: the other server refused the sign-in',
      }),
    ).toBe('From me on imap.old.example · 0 copied · 2 not copied · The other server refused the sign-in')
    expect(describeDetail({ action: 'mail.mailbox.exported', detail: '' })).toBe('')
    expect(describeDetail({ action: 'mail.mailbox.import-paused', detail: 'something else' })).toBe('something else')
  })
})
