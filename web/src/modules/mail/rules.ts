import type { RowStatus } from '../../shared/families/index.ts'
import type { MailAlias, MailClientSettings, MailDomain, Mailbox, MailRecord, RecordChange } from './types.ts'

export type Tone = 'positive' | 'neutral' | 'warning'

// ---- the domain ----

export type DomainState = { tone: Tone; label: string; sentence: string }

export function domainState(mail: Pick<MailDomain, 'domain' | 'active' | 'problem'>): DomainState {
  if (mail.active) {
    return { tone: 'positive', label: 'Receives mail', sentence: `Mail for addresses on ${mail.domain} arrives here.` }
  }
  return {
    tone: 'warning',
    label: 'Waiting for its mail records',
    sentence: `Mail for ${mail.domain} is received here once the domain's mail records point to this server. Until then its mailboxes can't send or receive.`,
  }
}

export function sidebarStatus(active: boolean): { status: RowStatus; text: string } {
  return active
    ? { status: 'live', text: 'Receives mail' }
    : { status: 'attention', text: 'Waiting for its mail records' }
}

// ---- sizes and limits ----

const GB = 1024 ** 3
const MB = 1024 ** 2

function trimNumber(value: number, digits: number): string {
  return value.toLocaleString('en', { maximumFractionDigits: digits })
}

export function formatBytes(bytes: number): string {
  if (bytes >= GB) return `${trimNumber(bytes / GB, bytes >= 10 * GB ? 0 : 1)} GB`
  if (bytes >= MB) return `${trimNumber(bytes / MB, bytes >= 10 * MB ? 0 : 1)} MB`
  if (bytes === 0) return '0 MB'
  if (bytes >= 1024) return `${trimNumber(bytes / 1024, 0)} KB`
  return `${bytes} bytes`
}

export function usageText(mailbox: Pick<Mailbox, 'quota_bytes' | 'usage'>): string {
  const quota = mailbox.quota_bytes > 0 ? formatBytes(mailbox.quota_bytes) : null
  if (!mailbox.usage) return quota ? `Room for ${quota}` : 'No storage limit'
  const used = formatBytes(mailbox.usage.used_bytes)
  return quota ? `${used} of ${quota} used` : `${used} used, no storage limit`
}

// Share of the quota in use, 0 to 1; null without a quota or usage.
export function usageShare(mailbox: Pick<Mailbox, 'quota_bytes' | 'usage'>): number | null {
  if (!mailbox.usage || mailbox.quota_bytes <= 0) return null
  return Math.min(1, mailbox.usage.used_bytes / mailbox.quota_bytes)
}

export function sendLimitsText(mailbox: Pick<Mailbox, 'send_per_hour' | 'send_per_day'>): string {
  return `Sends up to ${mailbox.send_per_hour.toLocaleString('en')} messages an hour, ${mailbox.send_per_day.toLocaleString('en')} a day`
}

export function bytesToGb(bytes: number): string {
  return bytes > 0 ? trimNumber(bytes / GB, 3) : ''
}

export type LimitsDraft = { quotaGb: string; perHour: number | null; perDay: number | null }
export type LimitsErrors = { quota?: string; perHour?: string; perDay?: string }

// The fields that changed, or the errors. An empty quota means "no limit", which only admins may choose.
export function limitsChange(
  current: Pick<Mailbox, 'quota_bytes' | 'send_per_hour' | 'send_per_day'>,
  draft: LimitsDraft,
  isAdmin: boolean,
): { change: { quota_bytes?: number; send_per_hour?: number; send_per_day?: number }; errors: LimitsErrors } {
  const errors: LimitsErrors = {}
  const change: { quota_bytes?: number; send_per_hour?: number; send_per_day?: number } = {}
  const text = draft.quotaGb.trim().replace(',', '.')
  if (text === '') {
    if (!isAdmin) errors.quota = 'Enter the storage in GB.'
    else if (current.quota_bytes !== 0) change.quota_bytes = 0
  } else {
    const gb = Number(text)
    if (!Number.isFinite(gb) || gb <= 0) errors.quota = 'Enter a number of GB, for example 2 or 0.5.'
    else {
      const bytes = Math.round(gb * GB)
      if (bytes !== current.quota_bytes) change.quota_bytes = bytes
    }
  }
  if (draft.perHour === null || !Number.isInteger(draft.perHour) || draft.perHour < 1) {
    errors.perHour = 'Enter a whole number of at least 1.'
  } else if (draft.perHour !== current.send_per_hour) change.send_per_hour = draft.perHour
  if (draft.perDay === null || !Number.isInteger(draft.perDay) || draft.perDay < 1) {
    errors.perDay = 'Enter a whole number of at least 1.'
  } else if (draft.perDay !== current.send_per_day) change.send_per_day = draft.perDay
  return { change, errors }
}

// ---- addresses ----

const LOCAL_PART = /^[a-z0-9._+-]{1,64}$/

export function validateLocalPart(value: string): string | null {
  const local = value.trim().toLowerCase()
  if (!local) return 'Enter the part before the @.'
  if (local.includes('@')) return 'Enter only the part before the @.'
  if (local.length > 64) return 'Use at most 64 characters.'
  if (!LOCAL_PART.test(local)) return 'Use letters, digits and . - _ + only, without spaces.'
  if (local.startsWith('.') || local.endsWith('.') || local.includes('..')) {
    return "A dot can't come first, last or twice in a row."
  }
  return null
}

const ADDRESS = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export function parseTargets(text: string): { targets: string[]; error: string | null } {
  const targets: string[] = []
  for (const part of text.split(/[\s,;]+/)) {
    const value = part.trim().toLowerCase()
    if (!value) continue
    if (!ADDRESS.test(value)) return { targets, error: `${part.trim()} is not an e-mail address.` }
    if (!targets.includes(value)) targets.push(value)
  }
  if (targets.length === 0) return { targets, error: 'Enter at least one address to deliver to.' }
  if (targets.length > 50) return { targets, error: 'Enter at most 50 addresses.' }
  return { targets, error: null }
}

export function aliasTitle(alias: Pick<MailAlias, 'catch_all' | 'address' | 'domain'>): string {
  return alias.catch_all ? `All other addresses @${alias.domain}` : alias.address
}

// Catch-all last, the rest by address.
export function sortAliases<T extends Pick<MailAlias, 'catch_all' | 'address'>>(aliases: T[]): T[] {
  return [...aliases].sort((a, b) =>
    a.catch_all !== b.catch_all ? (a.catch_all ? 1 : -1) : a.address.localeCompare(b.address),
  )
}

// ---- settings for mail programs ----

export type ClientRow = { label: string; value: string; copy?: boolean }

export function clientRows(client: MailClientSettings): ClientRow[] {
  return [
    { label: 'Server (incoming and outgoing)', value: client.server, copy: true },
    { label: 'User name', value: client.user_name_is_address ? 'The full e-mail address' : 'The mailbox name' },
    { label: 'Password', value: 'The password of the mailbox' },
    { label: 'Incoming mail (IMAP)', value: `Port ${client.imap_port}, SSL/TLS` },
    {
      label: 'Outgoing mail (SMTP)',
      value: `Port ${client.smtp_port}, SSL/TLS, or port ${client.smtp_starttls_port}, STARTTLS`,
    },
  ]
}

// ---- DNS records ----

const KIND_LABEL: Record<string, string> = {
  mx: 'Where mail for the domain goes',
  spf: 'Which servers may send for the domain',
  dkim: 'Signature of sent mail',
  dmarc: 'What receivers do with forged mail',
  'srv-submissions': 'Lets mail programs find the server for sending',
  'srv-imaps': 'Lets mail programs find the server for receiving',
  autoconfig: 'Lets mail programs set themselves up',
  autodiscover: 'Lets Outlook set itself up',
}

export function recordPurpose(kind: string): string {
  return KIND_LABEL[kind] ?? 'Mail record'
}

const STATUS: Record<string, { tone: Tone; label: string }> = {
  ok: { tone: 'positive', label: 'Set up' },
  missing: { tone: 'warning', label: 'Missing' },
  different: { tone: 'warning', label: 'Has another value' },
  conflict: { tone: 'warning', label: 'Clashes with an existing record' },
  unknown: { tone: 'neutral', label: "Couldn't check" },
}

export function recordStatus(status: string, optional = false): { tone: Tone; label: string } {
  if (optional && (status === 'missing' || status === 'different')) return { tone: 'neutral', label: 'Recommended' }
  return STATUS[status] ?? STATUS.unknown
}

// The name as people type it at a DNS provider: "@" for the domain itself.
export function recordName(record: Pick<MailRecord, 'name'>): string {
  return record.name === '' ? '@' : record.name
}

// Records mail needs that aren't set up yet; recommended ones are counted by recommendedOpen.
export function recordsNeedWork(records: Pick<MailRecord, 'status' | 'optional'>[]): number {
  return records.filter((record) => !record.optional && record.status !== 'ok').length
}

export function recommendedOpen(records: Pick<MailRecord, 'status' | 'optional'>[]): number {
  return records.filter((record) => record.optional && record.status !== 'ok').length
}

const ACTION_LABEL: Record<string, string> = {
  create: 'Add',
  replace: 'Replace',
  add: 'Add next to the existing one',
  keep: 'Already right',
  skip: 'Left as it is',
}

export function changeLabel(action: string): string {
  return ACTION_LABEL[action] ?? action
}

// What a change that needs a yes means for the person.
export function changeSentence(change: Pick<RecordChange, 'kind' | 'action' | 'old'>, domain: string): string {
  if (change.kind === 'mx' && change.action === 'replace') {
    const old = change.old.length > 0 ? ` (now ${change.old.join(', ')})` : ''
    return `Mail for ${domain} currently goes to another server${old}. After this change it comes here, and the other server gets no more new mail.`
  }
  if (change.action === 'replace') return `An existing record of ${domain} is replaced.`
  return `A record of ${domain} is changed.`
}

export function changesToConfirm(changes: RecordChange[]): RecordChange[] {
  return changes.filter((change) => change.needs_confirm)
}

const HOST = /^(?=.{1,253}$)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$/

export function validateMailDomain(value: string): string | null {
  const domain = value.trim().toLowerCase().replace(/\.$/, '')
  if (!domain) return 'Enter a domain.'
  if (domain.includes('@')) return 'Enter only the domain, the part after the @.'
  if (!HOST.test(domain)) return 'Enter a domain name like example.com.'
  return null
}

// ---- sender name, app passwords and organisations ----

export const DISPLAY_NAME_MAX = 64
export const APP_PASSWORD_NAME_MAX = 64
export const ORGANISATION_NAME_MAX = 64

// The sender's name as the server accepts it: one line, at most 64
// characters, none of < > " ; (they would break the sender line). Empty removes it.
export function validateDisplayName(value: string): string | null {
  const name = value.trim()
  if (/[\r\n]/.test(name)) return 'Use a single line.'
  if ([...name].length > DISPLAY_NAME_MAX) return `Use at most ${DISPLAY_NAME_MAX} characters.`
  if (/[<>";]/.test(name)) return 'Leave out < > " and ;.'
  return null
}

export function validateAppPasswordName(value: string): string | null {
  const name = value.trim()
  if (!name) return 'Enter the device or program, for example "Phone".'
  if ([...name].length > APP_PASSWORD_NAME_MAX) return `Use at most ${APP_PASSWORD_NAME_MAX} characters.`
  return null
}

export function validateOrganisationName(value: string): string | null {
  const name = value.trim()
  if (!name) return 'Enter a name, for example the name of the company.'
  if ([...name].length > ORGANISATION_NAME_MAX) return `Use at most ${ORGANISATION_NAME_MAX} characters.`
  return null
}

// "Not used yet" or "Last used 25 Nov 2026, 10:00 from 203.0.113.0/24".
export function appPasswordUse(
  app: { last_used_at: string | null; last_used_network: string },
  formatDateTime: (value: string) => string,
): string {
  if (!app.last_used_at) return 'Not used yet'
  const where = app.last_used_network ? ` from ${app.last_used_network}` : ''
  return `Last used ${formatDateTime(app.last_used_at)}${where}`
}

// The sentence about a domain's organisation on its page.
export function organisationSentence(organisation: MailDomain['organisation']): string {
  if (!organisation) return 'This domain stands alone.'
  return `Part of ${organisation.name}.`
}

export function organisationDomainsText(organisation: MailDomain['organisation']): string {
  if (!organisation) return ''
  if (organisation.domains.length === 0) return 'No other domains you can see belong to it.'
  return `Together with ${organisation.domains.join(', ')}.`
}
