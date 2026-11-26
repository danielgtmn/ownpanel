import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setMailboxForwarding, setMailboxMayForward, setMailboxOutOfOffice } from './api.ts'
import { MailboxRulesForms } from './MailboxRulesPanel.tsx'
import { forwardingStatus, outOfOfficeStatus } from './rules.ts'
import type { Mailbox, MailboxRules } from './types.ts'

const day = (value: string) => value
const at = () => '25 Nov 2026'

const rules: MailboxRules = {
  out_of_office: {
    enabled: true,
    on: true,
    start: '2026-11-20',
    end: '',
    set_by: '',
    set_at: null,
    subject: '',
    text: '',
    days: 0,
  },
  forwarding: { enabled: true, to: 'bob@example.com', keep_copy: true, set_by: 'admin', set_at: '2026-11-25T10:00:00Z' },
  hand_written: true,
  may_forward: false,
}

const mailbox = {
  id: 7,
  address: 'anna@example.com',
  domain: 'example.com',
  local_part: 'anna',
} as Mailbox

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('out-of-office and forwarding in the panel', () => {
  it('keeps what the person set private and names who set it from the panel', () => {
    expect(outOfOfficeStatus(rules.out_of_office, day, at)).toBe(
      'On (from 2026-11-20). Set by the person; its text is private.',
    )
    expect(outOfOfficeStatus({ ...rules.out_of_office, on: false, end: '2026-12-01' }, day, at)).toBe(
      'Turned on, but not answering today (2026-11-20 to 2026-12-01). Set by the person; its text is private.',
    )
    expect(outOfOfficeStatus({ ...rules.out_of_office, enabled: false }, day, at)).toBe('Off.')
    expect(forwardingStatus(rules.forwarding, at)).toBe('On, to bob@example.com. Set by admin on 25 Nov 2026.')
    expect(forwardingStatus({ ...rules.forwarding, set_by: '' }, at)).toBe(
      'On. Set by the person; the address is private.',
    )
  })

  it('shows the forms with the notes on replacing and on forwarding being off', () => {
    const html = renderToStaticMarkup(
      <MailboxRulesForms mailbox={mailbox} rules={rules} onRules={() => undefined} />,
    ).replaceAll('&#x27;', "'")
    expect(html).toContain('the activity log records it')
    expect(html).toContain("The person's filters were written in a mail program.")
    expect(html).toContain('Saving here replaces the reply the person wrote.')
    expect(html).toContain('Forwarding is turned off for this mailbox.')
    expect(html).toContain('Filters of this mailbox may forward mail')
    expect(html).toContain('value="bob@example.com"')
    expect(html).not.toMatch(/sieve|\bscripts?\b/i)
  })

  it('sends exactly the fields the panel knows', async () => {
    const fetch = vi.fn(async (..._args: unknown[]) => new Response(JSON.stringify(rules), { status: 200 }))
    vi.stubGlobal('fetch', fetch)
    const reply = { enabled: true, subject: 'Away', text: 'Gone.', start: '', end: '', zone: '+0100', days: 7 }
    await setMailboxOutOfOffice(7, reply, true)
    await setMailboxForwarding(7, { enabled: false, to: '', keep_copy: false }, false)
    vi.stubGlobal('fetch', vi.fn(async (..._args: unknown[]) => new Response(null, { status: 204 })))
    await setMailboxMayForward(7, false)
    const sent = fetch.mock.calls.map(([url, init]) => [url, JSON.parse((init as RequestInit).body as string)])
    expect(sent).toEqual([
      ['/api/mail/mailboxes/7/out-of-office', { ...reply, replace: true }],
      ['/api/mail/mailboxes/7/forwarding', { enabled: false, to: '', keep_copy: false, replace: false }],
    ])
  })
})
