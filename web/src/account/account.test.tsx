import { Dialog } from '@ui-sdk/react/dialog'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ChangePasswordForm, ShareForm } from './AccountDialogs.tsx'
import { AccountPage } from './AccountPage.tsx'
import type { Me } from './api.ts'
import { SignInForm } from './SignInForm.tsx'

const noop = () => undefined

const me: Me = {
  address: 'anna@example.com',
  display_name: 'Anna Example',
  used_bytes: 100 * 1024 ** 2,
  messages: 12,
  quota_bytes: 1024 ** 3,
  app_passwords: [
    { id: 'abcdefghijklmnop', name: 'Phone', created_at: '2026-11-20T10:00:00Z', last_used_at: null, last_used_network: '' },
  ],
  app_passwords_only: false,
  app_passwords_available: true,
  uses_known_days: 30,
  client: { server: 'mail.example.com', imap_port: 993, smtp_port: 465, smtp_starttls_port: 587, user_name_is_address: true },
  shared_with: [],
  access: [],
}

const sharing: Me = {
  ...me,
  shared_with: [{ address: 'ben@example.com', display_name: 'Ben Assistant', rights: ['read', 'write', 'send-on-behalf'] }],
  access: [
    {
      address: 'info@example.com',
      display_name: 'Info desk',
      shared: true,
      rights: ['read', 'send-as'],
      folder: 'Shared/info@example.com',
    },
  ],
}

describe('account area', () => {
  it('signs in with address and password, the password can be shown', () => {
    const html = renderToStaticMarkup(<SignInForm host="mail.example.com" notice="You signed out." onSignedIn={noop} />)
    expect(html).toContain('E-mail address')
    expect(html).toMatch(/autocomplete="username"/i)
    expect(html).toMatch(/autocomplete="current-password"/i)
    expect(html).toContain('Show password')
    expect(html).toContain('You signed out.')
  })

  it('shows the account with storage, app passwords, settings and the note about resets', () => {
    const html = renderToStaticMarkup(<AccountPage host="mail.example.com" me={me} onChange={noop} onSignedOut={noop} />)
    expect(html).toContain('anna@example.com')
    expect(html).toContain('100 MB of 1 GB used')
    expect(html).toContain('Phone')
    expect(html).toContain('Not used yet')
    expect(html).toContain('Only allow mail programs with app passwords')
    expect(html).toContain('href="/account/api/apple-setup"')
    expect(html).toContain('The people who manage this server can reset your password.')
    expect(html).not.toMatch(/argon|cookie|session|HMAC|JSON/i)
  })

  it('shows nobody sharing and no other mailboxes plainly', () => {
    const html = renderToStaticMarkup(<AccountPage host="mail.example.com" me={me} onChange={noop} onSignedOut={noop} />)
    expect(html).toContain('Your mailbox is shared with')
    expect(html).toContain('Nobody. Only you can use your mailbox.')
    expect(html).toContain('Share with someone')
    expect(html).toContain('You have access to')
    expect(html).toContain('No other mailboxes.')
  })

  it('lists who the mailbox is shared with and the mailboxes the person may use', () => {
    const html = renderToStaticMarkup(
      <AccountPage host="mail.example.com" me={sharing} onChange={noop} onSignedOut={noop} />,
    )
    expect(html).toContain('ben@example.com')
    expect(html).toContain('Ben Assistant')
    expect(html).toContain('Read and write, Send on behalf')
    expect(html).toContain('info@example.com')
    expect(html).toContain('Shared mailbox')
    expect(html).toContain('Read, Send as')
    expect(html).toContain('In your mail program: the folder Shared/info@example.com')
    const sections = html.slice(html.indexOf('Your mailbox is shared with'), html.indexOf('Settings for mail programs'))
    expect(sections).not.toMatch(/imap|acl|sieve|grant|delegat/i)
  })

  it('asks for an address and rights when sharing, and keeps Read while Write is on', () => {
    const add = renderToStaticMarkup(
      <Dialog open>
        <ShareForm own="anna@example.com" existing={null} onSubmit={async () => undefined} />
      </Dialog>,
    )
    expect(add).toContain('Their e-mail address')
    expect(add).toContain('Manage filters and out-of-office')
    expect(add).toContain('Send on behalf')
    const change = renderToStaticMarkup(
      <Dialog open>
        <ShareForm
          own="anna@example.com"
          existing={{ address: 'ben@example.com', display_name: '', rights: ['write'] }}
          onSubmit={async () => undefined}
        />
      </Dialog>,
    )
    expect(change).not.toContain('Their e-mail address')
    expect(change).toContain('ben@example.com')
    // Read is ticked and locked because Write is on.
    expect(change.match(/data-disabled/g)?.length ?? 0).toBeGreaterThan(0)
  })

  it('offers to sign out app password programs only when there are some', () => {
    const withApps = renderToStaticMarkup(
      <Dialog open>
        <ChangePasswordForm hasAppPasswords onSubmit={async () => undefined} />
      </Dialog>,
    )
    expect(withApps).toContain('Also sign out mail programs that use app passwords')
    expect(withApps).toContain('Repeat new password')
    const without = renderToStaticMarkup(
      <Dialog open>
        <ChangePasswordForm hasAppPasswords={false} onSubmit={async () => undefined} />
      </Dialog>,
    )
    expect(without).not.toContain('Also sign out')
  })
})
