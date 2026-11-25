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
