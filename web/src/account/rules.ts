import { validatePasswordPair } from '../shared/session/validation.ts'
import type { AppPassword, ClientSettings, Me } from './api.ts'

// The same limits as the mail server's (internal/mail/config, internal/passwd).
export const NAME_MAX = 64

const ADDRESS = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export function validateAddress(value: string): string | null {
  const address = value.trim()
  if (!address) return 'Enter your e-mail address.'
  if (!ADDRESS.test(address)) return 'Enter your whole e-mail address, like anna@example.com.'
  return null
}

// One line, at most 64 characters, none of < > " (they would break the sender line). Empty removes the name.
export function validateDisplayName(value: string): string | null {
  const name = value.trim()
  if (/[\r\n]/.test(name)) return 'Use a single line.'
  if ([...name].length > NAME_MAX) return `Use at most ${NAME_MAX} characters.`
  if (/[<>"]/.test(name)) return 'Leave out < > and ".'
  return null
}

export function validateAppPasswordName(value: string): string | null {
  const name = value.trim()
  if (!name) return 'Enter the device or program, for example "Phone".'
  if ([...name].length > NAME_MAX) return `Use at most ${NAME_MAX} characters.`
  if (/[<>"\r\n]/.test(name)) return 'Use a single line without < > and ".'
  return null
}

// Passwords of the form of an app password would only ever be checked as one.
const APP_PASSWORD = /^op_[a-z2-7]{16}_[a-z2-7]{32}$/

export function looksLikeAppPassword(value: string): boolean {
  return APP_PASSWORD.test(value)
}

const GB = 1024 ** 3
const MB = 1024 ** 2

function trim(value: number, digits: number): string {
  return value.toLocaleString('en', { maximumFractionDigits: digits })
}

export function formatBytes(bytes: number): string {
  if (bytes >= GB) return `${trim(bytes / GB, bytes >= 10 * GB ? 0 : 1)} GB`
  if (bytes >= MB) return `${trim(bytes / MB, bytes >= 10 * MB ? 0 : 1)} MB`
  if (bytes === 0) return '0 MB'
  if (bytes >= 1024) return `${trim(bytes / 1024, 0)} KB`
  return `${bytes} bytes`
}

export function usageText(me: Pick<Me, 'used_bytes' | 'quota_bytes'>): string {
  const used = formatBytes(me.used_bytes)
  return me.quota_bytes > 0 ? `${used} of ${formatBytes(me.quota_bytes)} used` : `${used} used, no storage limit`
}

// Share of the storage limit in use, 0 to 1; null without a limit.
export function usageShare(me: Pick<Me, 'used_bytes' | 'quota_bytes'>): number | null {
  if (me.quota_bytes <= 0) return null
  return Math.min(1, me.used_bytes / me.quota_bytes)
}

export function messagesText(count: number): string {
  return count === 1 ? '1 message' : `${count.toLocaleString('en')} messages`
}

// "Last used 25 Nov 2026, 10:00 from 203.0.113.0/24", or when it wasn't used as far as the server remembers.
export function appPasswordUse(
  app: Pick<AppPassword, 'created_at' | 'last_used_at' | 'last_used_network'>,
  knownDays: number,
  formatDateTime: (value: string) => string,
  now: Date = new Date(),
): string {
  if (app.last_used_at) {
    const where = app.last_used_network ? ` from ${app.last_used_network}` : ''
    return `Last used ${formatDateTime(app.last_used_at)}${where}`
  }
  const created = new Date(app.created_at).getTime()
  if (Number.isFinite(created) && now.getTime() - created > knownDays * 24 * 3600 * 1000) {
    return `Not used in the last ${knownDays} days`
  }
  return 'Not used yet'
}

export type ClientRow = { label: string; value: string; copy?: boolean }

export function clientRows(client: ClientSettings, address: string, appPasswordsOnly: boolean): ClientRow[] {
  return [
    { label: 'Server (incoming and outgoing)', value: client.server, copy: true },
    { label: 'User name', value: client.user_name_is_address ? address : 'The mailbox name', copy: true },
    {
      label: 'Password',
      value: appPasswordsOnly ? 'An app password' : 'Your mailbox password, or better an app password',
    },
    { label: 'Incoming mail (IMAP)', value: `Port ${client.imap_port}, SSL/TLS` },
    {
      label: 'Outgoing mail (SMTP)',
      value: `Port ${client.smtp_port}, SSL/TLS, or port ${client.smtp_starttls_port}, STARTTLS`,
    },
  ]
}

export type PasswordErrors = { current?: string; password?: string; repeat?: string }

// The checks of the password dialog before anything is sent: the same length rules as the panel's.
export function passwordErrors(current: string, password: string, repeat: string): PasswordErrors {
  const errors: PasswordErrors = { ...validatePasswordPair(password, repeat) }
  if (!current) errors.current = 'Enter your current password.'
  if (!errors.password && looksLikeAppPassword(password)) {
    errors.password = 'This looks like an app password. Choose another password.'
  }
  if (!errors.password && current && password === current) {
    errors.password = 'Choose a password different from the current one.'
  }
  return errors
}

export type SignInErrors = { address?: string; password?: string; form?: string }

export function signInErrors(address: string, password: string): SignInErrors {
  const errors: SignInErrors = {}
  const addressError = validateAddress(address)
  if (addressError) errors.address = addressError
  if (!password) errors.password = 'Enter your password.'
  return errors
}
