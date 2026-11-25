import { describe, expect, it } from 'vitest'
import { settingsFixture } from '../../shared/settings/testing.ts'
import { parseLists, spamChange, spamDraft, spamSummary, validateSpamDraft } from './spamChecks.ts'

describe('spam checks', () => {
  it('reads the three choices for the lists from the settings', () => {
    expect(spamDraft(settingsFixture()).mode).toBe('default')
    expect(
      spamDraft(
        settingsFixture({
          mail_blocklists_default: false,
          mail_blocklists: [],
        }),
      ).mode,
    ).toBe('off')
    const own = spamDraft(
      settingsFixture({
        mail_blocklists_default: false,
        mail_blocklists: ['a.example', '••••.zen.dq.spamhaus.net'],
      }),
    )
    expect(own).toMatchObject({
      mode: 'own',
      lists: 'a.example\n••••.zen.dq.spamhaus.net',
    })
  })

  it('splits names on lines, spaces and commas', () => {
    expect(parseLists(' A.example.\n b.example, c.example \n\n')).toEqual(['a.example', 'b.example', 'c.example'])
  })

  it('sends only what changed', () => {
    const settings = settingsFixture()
    expect(spamChange(spamDraft(settings), settings)).toEqual({})
    expect(spamChange({ ...spamDraft(settings), mode: 'off' }, settings)).toEqual({ mail_blocklists: [] })
    expect(
      spamChange(
        {
          ...spamDraft(settings),
          mode: 'own',
          lists: 'zen.spamhaus.org\nb.example',
        },
        settings,
      ),
    ).toEqual({
      mail_blocklists: ['zen.spamhaus.org', 'b.example'],
    })
    expect(spamChange({ ...spamDraft(settings), junkScore: 7, greylisting: true }, settings)).toEqual({
      mail_junk_score: 7,
      mail_greylisting: true,
    })
    const off = settingsFixture({
      mail_blocklists_default: false,
      mail_blocklists: [],
    })
    expect(spamChange({ ...spamDraft(off), mode: 'default' }, off)).toEqual({
      mail_blocklists_default: true,
    })
    // A hidden key is sent back as it came, which keeps it.
    const own = settingsFixture({
      mail_blocklists_default: false,
      mail_blocklists: ['••••.zen.dq.spamhaus.net'],
    })
    expect(spamChange(spamDraft(own), own)).toEqual({})
    expect(spamChange({ ...spamDraft(own), lists: '••••.zen.dq.spamhaus.net\na.example' }, own)).toEqual({
      mail_blocklists: ['••••.zen.dq.spamhaus.net', 'a.example'],
    })
  })

  it('checks names and the points before saving', () => {
    const base = spamDraft(settingsFixture())
    expect(validateSpamDraft(base)).toEqual({})
    expect(validateSpamDraft({ ...base, mode: 'own', lists: '' }).lists).toMatch(/at least one/)
    expect(validateSpamDraft({ ...base, mode: 'own', lists: 'not_a list' }).lists).toMatch(/isn't a list name/)
    expect(validateSpamDraft({ ...base, mode: 'own', lists: 'a.example\na.example' }).lists).toMatch(/twice/)
    expect(
      validateSpamDraft({
        ...base,
        mode: 'own',
        lists: '••••.zen.dq.spamhaus.net',
      }),
    ).toEqual({})
    const many = Array.from({ length: 11 }, (_, i) => `l${i}.example`).join('\n')
    expect(validateSpamDraft({ ...base, mode: 'own', lists: many }).lists).toMatch(/at most 10/)
    expect(validateSpamDraft({ ...base, junkScore: null }).junkScore).toBe('Enter a number.')
    expect(validateSpamDraft({ ...base, junkScore: 0.5 }).junkScore).toMatch(/from 1 to 50/)
    expect(validateSpamDraft({ ...base, mode: 'off', lists: 'garbage here' })).toEqual({})
  })
})

describe('spamSummary', () => {
  it('describes the checks in one sentence', () => {
    const base = settingsFixture({
      mail_blocklists_default: true,
      mail_blocklists: ['zen.spamhaus.org'],
      mail_junk_score: 5,
      mail_greylisting: false,
    })
    expect(spamSummary(base)).toBe(
      'Checks the recommended list of spam senders. Junk from 5 points; first mail from unknown servers is not held back.',
    )
    expect(spamSummary({ ...base, mail_blocklists_default: false, mail_blocklists: [], mail_greylisting: true })).toBe(
      "Doesn't check lists of spam senders. Junk from 5 points; first mail from unknown servers is held back.",
    )
    expect(spamSummary({ ...base, mail_blocklists_default: false, mail_blocklists: ['a.example', 'b.example'] })).toMatch(
      /^Checks 2 own lists of spam senders\./,
    )
  })
})
