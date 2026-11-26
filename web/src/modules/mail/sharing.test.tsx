import { Dialog } from '@ui-sdk/react/dialog'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AliasDialog } from './AliasDialog.tsx'
import { CreateMailboxDialog } from './MailboxDialogs.tsx'
import { GrantForm, PersonalAccessPanel, SharedAccessPanel } from './MailboxSharing.tsx'
import type { Mailbox } from './types.ts'

const done = async () => undefined

const base: Mailbox = {
  id: 7,
  address: 'info@example.com',
  domain: 'example.com',
  local_part: 'info',
  owner: 'owner',
  quota_bytes: 0,
  send_per_hour: 100,
  send_per_day: 1000,
  created_at: '2026-11-20T10:00:00Z',
  usage: null,
  display_name: '',
  app_passwords_only: false,
  kind: 'shared',
  keep_sent_copy: true,
  shared_with: [
    { mailbox_id: 1, address: 'anna@example.com', display_name: 'Anna', rights: ['read', 'write'], active: true },
    { mailbox_id: 2, address: 'ben@other.org', display_name: '', rights: ['read', 'send-as'], active: false },
  ],
  access_to: [],
}

describe('sharing in the panel', () => {
  it('lists the people with access of a shared mailbox, with a hint when one does not count', () => {
    const html = renderToStaticMarkup(<SharedAccessPanel mailbox={base} onMailboxChanged={done} />)
    expect(html).toContain('anna@example.com')
    expect(html).toContain('Read and write')
    expect(html).toContain('Read, Send as')
    expect(html).toContain('Not in effect')
    expect(html).toContain('Give a person access')
    expect(html).toContain('Keep a copy of mail sent for this mailbox in its Sent folder')
    expect(html).not.toMatch(/imap|acl|sieve|grant|storage/i)
  })

  it('shows what a personal mailbox may use and who it is shared with, with only a remove action', () => {
    const personal: Mailbox = {
      ...base,
      address: 'anna@example.com',
      kind: 'personal',
      shared_with: [{ mailbox_id: 3, address: 'carl@example.com', display_name: '', rights: ['read'], active: true }],
      access_to: [{ mailbox_id: 7, address: 'info@example.com', kind: 'shared', rights: ['read', 'manage'], active: true }],
    }
    const html = renderToStaticMarkup(<PersonalAccessPanel mailbox={personal} onMailboxChanged={done} />)
    expect(html).toContain('Has access to')
    expect(html).toContain('info@example.com')
    expect(html).toContain('Manage filters and out-of-office')
    expect(html).toContain('Shared with')
    expect(html).toContain('carl@example.com')
    expect(html).toContain('own account settings')
    expect(html).not.toContain('Change<')
  })

  it('asks for the person and the rights', () => {
    const html = renderToStaticMarkup(
      <Dialog open>
        <GrantForm mailbox={base} existing={null} onSubmit={done} />
      </Dialog>,
    )
    expect(html).toContain('The person’s mailbox')
    expect(html).toContain('Send on behalf')
    expect(html).toContain('Give access')
  })

  it('offers a shared mailbox when creating one', () => {
    const html = renderToStaticMarkup(
      <CreateMailboxDialog open onOpenChange={() => undefined} domain="example.com" onSubmit={done} />,
    )
    expect(html).toContain('Personal mailbox')
    expect(html).toContain('Shared mailbox')
    expect(html).toContain('nobody signs in to it directly')
  })

  it('asks who may write to a forwarding address', () => {
    const html = renderToStaticMarkup(
      <AliasDialog
        open
        onOpenChange={() => undefined}
        domain="example.com"
        existing={null}
        catchAllTaken={false}
        onSubmit={done}
      />,
    )
    expect(html).toContain('Who may write to this address')
    expect(html).toContain('Only addresses of this organisation')
    expect(html).toContain('Only its members')
    expect(html).toContain('Hide from the address book')
  })
})
