import { describe, expect, it } from 'vitest'
import {
  appPasswordUse,
  clientRows,
  passwordChangedNotice,
  passwordErrors,
  signInErrors,
  usageShare,
  usageText,
  validateAppPasswordName,
  validateDisplayName,
} from './rules.ts'

const fmt = (value: string) => `<${value}>`

describe('sign-in form', () => {
  it('needs a whole address and a password', () => {
    expect(signInErrors('', '')).toEqual({ address: 'Enter your e-mail address.', password: 'Enter your password.' })
    expect(signInErrors('anna', 'x').address).toMatch(/whole e-mail address/)
    expect(signInErrors(' anna@example.com ', 'secret')).toEqual({})
  })
})

describe('password dialog', () => {
  it('asks for the current password and the panel’s length rules', () => {
    expect(passwordErrors('', 'long-enough-1', 'long-enough-1')).toEqual({ current: 'Enter your current password.' })
    expect(passwordErrors('old-password', 'short', 'short').password).toMatch(/at least 10/)
    expect(passwordErrors('old-password', 'long-enough-1', 'long-enough-2').repeat).toMatch(/don't match/)
    expect(passwordErrors('old-password', 'long-enough-1', 'long-enough-1')).toEqual({})
  })

  it('refuses the form of an app password and the same password again', () => {
    const app = 'op_abcdefghijklmnop_abcdefghijklmnopqrstuvwxyz234567'
    expect(passwordErrors('old-password', app, app).password).toMatch(/app password/)
    expect(passwordErrors('old-password', 'old-password', 'old-password').password).toMatch(/different/)
  })
})

describe('names', () => {
  it('takes one line of at most 64 characters without < > "', () => {
    expect(validateDisplayName('')).toBeNull()
    expect(validateDisplayName('Anna Example')).toBeNull()
    expect(validateDisplayName('a\nb')).toMatch(/single line/)
    expect(validateDisplayName('x'.repeat(65))).toMatch(/at most 64/)
    expect(validateDisplayName('Anna <a@b>')).toMatch(/Leave out/)
    expect(validateAppPasswordName(' ')).toMatch(/Enter the device/)
    expect(validateAppPasswordName('Phone')).toBeNull()
    expect(validateAppPasswordName('a"b')).toMatch(/without/)
  })
})

describe('storage', () => {
  it('reads with and without a limit', () => {
    expect(usageText({ used_bytes: 512 * 1024 ** 2, quota_bytes: 2 * 1024 ** 3 })).toBe('512 MB of 2 GB used')
    expect(usageText({ used_bytes: 0, quota_bytes: 0 })).toBe('0 MB used, no storage limit')
    expect(usageShare({ used_bytes: 1, quota_bytes: 0 })).toBeNull()
    expect(usageShare({ used_bytes: 3, quota_bytes: 2 })).toBe(1)
  })
})

describe('app passwords', () => {
  const now = new Date('2026-11-25T12:00:00Z')
  it('says when one was last used, or that it was not', () => {
    expect(
      appPasswordUse(
        { created_at: '2026-11-01T00:00:00Z', last_used_at: '2026-11-25T10:00:00Z', last_used_network: '203.0.113.0/24' },
        30,
        fmt,
        now,
      ),
    ).toBe('Last used <2026-11-25T10:00:00Z> from 203.0.113.0/24')
    expect(appPasswordUse({ created_at: '2026-11-20T00:00:00Z', last_used_at: null, last_used_network: '' }, 30, fmt, now)).toBe(
      'Not used yet',
    )
    expect(appPasswordUse({ created_at: '2026-01-01T00:00:00Z', last_used_at: null, last_used_network: '' }, 30, fmt, now)).toBe(
      'Not used in the last 30 days',
    )
  })

  it('names the password to use in mail programs', () => {
    const client = { server: 'mail.example.com', imap_port: 993, smtp_port: 465, smtp_starttls_port: 587, user_name_is_address: true }
    const rows = clientRows(client, 'anna@example.com', true)
    expect(rows.find((row) => row.label === 'User name')?.value).toBe('anna@example.com')
    expect(rows.find((row) => row.label === 'Password')?.value).toBe('An app password')
    expect(clientRows(client, 'anna@example.com', false).find((row) => row.label === 'Password')?.value).toMatch(
      /mailbox password/,
    )
  })
})

describe('password changed', () => {
  it('says so, and warns when app passwords are left', () => {
    expect(passwordChangedNotice(false, undefined)).toEqual({ kind: 'success', text: 'Your password was changed.' })
    expect(passwordChangedNotice(true, undefined).text).toMatch(/all app passwords were revoked/)
    const warning = 'Your password was changed, but 1 app password could not be revoked. Revoke it below.'
    expect(passwordChangedNotice(true, warning)).toEqual({ kind: 'warning', text: warning })
  })
})
