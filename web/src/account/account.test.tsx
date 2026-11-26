import { Dialog } from '@ui-sdk/react/dialog'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ChangePasswordForm } from './AccountDialogs.tsx'
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
