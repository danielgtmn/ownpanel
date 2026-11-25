// Out-of-office reply and forwarding of a mailbox, as the account area and the
// panel edit them. The limits are the mail server's (internal/mail/config/rules.go).

export const SUBJECT_MAX = 200
export const TEXT_MAX_BYTES = 8 * 1024
export const DAYS_MIN = 1
export const DAYS_MAX = 90
export const DAYS_DEFAULT = 7

export type OutOfOfficeDraft = {
  enabled: boolean
  subject: string
  text: string
  // YYYY-MM-DD or "" for open.
  start: string
  end: string
  days: number | null
}

export type ForwardingDraft = { enabled: boolean; to: string; keepCopy: boolean }

export type OutOfOfficeBody = {
  enabled: boolean
  subject: string
  text: string
  start: string
  end: string
  zone: string
  days: number
}

export type ForwardingBody = { enabled: boolean; to: string; keep_copy: boolean }

type OutOfOfficeSource = {
  enabled: boolean
  subject?: string
  text?: string
  start?: string
  end?: string
  days?: number
}

export function outOfOfficeDraft(source: OutOfOfficeSource): OutOfOfficeDraft {
  return {
    enabled: source.enabled,
    subject: source.subject ?? '',
    text: source.text ?? '',
    start: source.start ?? '',
    end: source.end ?? '',
    days: source.days && source.days >= DAYS_MIN ? source.days : DAYS_DEFAULT,
  }
}

export function forwardingDraft(source: { enabled: boolean; to?: string; keep_copy?: boolean }): ForwardingDraft {
  return { enabled: source.enabled, to: source.to ?? '', keepCopy: source.keep_copy ?? false }
}

const DATE = /^(\d{4})-(\d{2})-(\d{2})$/

function pad(value: number): string {
  return String(value).padStart(2, '0')
}

export function isoDate(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

// The browser's offset from UTC on that day ("+hhmm"/"-hhmm"), so the reply starts and ends at local midnight
// even across a change to or from summer time. Without a day: today.
export function zoneOffset(day: string, now: Date = new Date()): string {
  const match = DATE.exec(day)
  const at = match ? new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]), 12) : now
  const minutes = -at.getTimezoneOffset()
  const sign = minutes < 0 ? '-' : '+'
  const abs = Math.abs(minutes)
  return `${sign}${pad(Math.floor(abs / 60))}${pad(abs % 60)}`
}

export function textBytes(text: string): number {
  return new TextEncoder().encode(text.replace(/\r\n/g, '\n')).length
}

export type OutOfOfficeErrors = { subject?: string; text?: string; dates?: string; days?: string }

export function outOfOfficeErrors(draft: OutOfOfficeDraft): OutOfOfficeErrors {
  const errors: OutOfOfficeErrors = {}
  const subject = draft.subject.trim()
  if (/[\r\n]/.test(subject)) errors.subject = 'Use a single line.'
  else if ([...subject].length > SUBJECT_MAX) errors.subject = `Use at most ${SUBJECT_MAX} characters.`
  if (textBytes(draft.text) > TEXT_MAX_BYTES) errors.text = 'The text is too long. Keep it under 8,000 characters.'
  else if (draft.enabled && !draft.text.trim()) errors.text = 'Write the reply people get.'
  if (draft.start && draft.end && draft.end < draft.start) errors.dates = 'The last day is before the first day.'
  if (draft.days === null || !Number.isInteger(draft.days) || draft.days < DAYS_MIN || draft.days > DAYS_MAX) {
    errors.days = `Enter a number of days from ${DAYS_MIN} to ${DAYS_MAX}.`
  }
  return errors
}

export function hasErrors(errors: object): boolean {
  return Object.values(errors).some(Boolean)
}

export function outOfOfficeBody(draft: OutOfOfficeDraft, now: Date = new Date()): OutOfOfficeBody {
  return {
    enabled: draft.enabled,
    subject: draft.subject.trim(),
    text: draft.text.replace(/\r\n/g, '\n'),
    start: draft.start,
    end: draft.end,
    zone: zoneOffset(draft.start, now),
    days: draft.days ?? DAYS_DEFAULT,
  }
}

const ADDRESS = /^[^\s@<>"]+@[^\s@<>"]+\.[^\s@<>"]+$/

export function validForwardAddress(value: string): boolean {
  return ADDRESS.test(value.trim())
}

export function forwardingError(draft: ForwardingDraft): string | null {
  const to = draft.to.trim()
  if (!draft.enabled && !to) return null
  if (!to) return 'Enter the address to forward to.'
  if (!validForwardAddress(to)) return 'Enter a whole e-mail address, like anna@example.com.'
  return null
}

export function forwardingBody(draft: ForwardingDraft): ForwardingBody {
  return { enabled: draft.enabled, to: draft.to.trim().toLowerCase(), keep_copy: draft.keepCopy }
}

// "Set by Anna on 25 Nov 2026, 10:00", or null when the person set it themselves.
export function setByText(
  setBy: string | undefined,
  setAt: string | null | undefined,
  formatDateTime: (value: string) => string,
): string | null {
  if (!setBy) return null
  return setAt ? `Set by ${setBy} on ${formatDateTime(setAt)}` : `Set by ${setBy}`
}

const dayFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' })

// A day like 2026-12-24 in the reader's own words, read as a local date (not midnight UTC).
export function formatDay(day: string): string {
  const match = DATE.exec(day)
  if (!match) return day
  return dayFormat.format(new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3])))
}

// When a reply that is turned on actually answers mail.
export function outOfOfficeWhen(draft: Pick<OutOfOfficeDraft, 'start' | 'end'>, formatDate: (day: string) => string) {
  if (draft.start && draft.end) return `From ${formatDate(draft.start)} to ${formatDate(draft.end)}`
  if (draft.start) return `From ${formatDate(draft.start)}`
  if (draft.end) return `Until ${formatDate(draft.end)}`
  return 'Until it is turned off'
}
