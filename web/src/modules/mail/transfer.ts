// Rules for moving mail into and out of a mailbox: the form for another
// mail account, the file to upload, its upload in parts, the folder choice
// and how import jobs are described.

import { ApiError } from '../../shared/api/index.ts'
import { formatBytes } from './rules.ts'
import type { ImportCheck, ImportChoices, ImportFolder, MailImport } from './types.ts'

// The server takes files up to 20 GiB in parts of at most 256 MiB. Smaller
// parts show progress more often and lose less when a connection breaks.
export const MAX_IMPORT_BYTES = 20 * 1024 ** 3
export const MAX_PART_BYTES = 256 * 1024 ** 2
export const PART_BYTES = 32 * 1024 ** 2

// ---- another mail account ----

export type SourceForm = { host: string; port: number; username: string; password: string }
export type SourceErrors = { host?: string; username?: string; password?: string }

export const SECURE_PORT = 993
export const UPGRADE_PORT = 143

const HOST_RE = /^[a-z0-9_-]+(\.[a-z0-9_-]+)*$/

// Mail services that need a sign-in through their own page, which the
// import can't do yet.
const UNSUPPORTED = /(^|\.)(outlook\.com|office365\.com|hotmail\.com|live\.com|outlook\.office\.com)$/

export function normaliseHost(value: string): string {
  return value.trim().toLowerCase().replace(/\.$/, '')
}

export function validateSource(form: SourceForm): SourceErrors {
  const errors: SourceErrors = {}
  const host = normaliseHost(form.host)
  if (!host) errors.host = 'Enter the name of the other mail server, like imap.example.com.'
  else if (host.includes('@')) errors.host = 'Enter the name of the server here, not an e-mail address.'
  else if (host.includes('://') || host.includes('/')) errors.host = 'Enter only the server name, like imap.example.com.'
  else if (!HOST_RE.test(host) || host.length > 253) errors.host = "That doesn't look like a server name."
  else if (UNSUPPORTED.test(host)) {
    errors.host = "Mail from Microsoft 365 and Outlook.com can't be brought over this way yet."
  }
  if (!form.username.trim()) errors.username = 'Enter the user name, usually the full e-mail address.'
  else if (/[\r\n]/.test(form.username) || form.username.length > 1024) errors.username = "That user name can't be used."
  if (!form.password) errors.password = 'Enter the password of the other account.'
  else if (/[\r\n]/.test(form.password) || form.password.length > 1024) errors.password = "That password can't be used."
  return errors
}

export function hasErrors(errors: object): boolean {
  return Object.values(errors).some(Boolean)
}

type Provider = { match: RegExp; server: string; hint?: string }

const PROVIDERS: Provider[] = [
  {
    match: /^(gmail|googlemail)\.com$/,
    server: 'imap.gmail.com',
    hint: 'Google needs an app password here, not your normal password: turn on 2-step verification, then create one under Security › App passwords in your Google account.',
  },
  {
    match: /^(yahoo|ymail)\.[a-z.]+$/,
    server: 'imap.mail.yahoo.com',
    hint: 'Yahoo needs an app password here: create one under Account security in your Yahoo account.',
  },
  { match: /^(gmx\.(de|net|at|ch)|gmx\.com)$/, server: 'imap.gmx.net' },
  { match: /^web\.de$/, server: 'imap.web.de' },
  { match: /^(icloud|me|mac)\.com$/, server: 'imap.mail.me.com', hint: 'iCloud needs an app-specific password, created on account.apple.com.' },
  {
    match: /^(outlook|hotmail|live)\.[a-z.]+$/,
    server: '',
    hint: "Mail from Outlook.com and Microsoft 365 can't be brought over this way yet. Export it from Outlook as a file instead, or move it with a mail program.",
  },
]

function providerFor(value: string): Provider | null {
  const domain = value.trim().toLowerCase().split('@')[1] ?? ''
  if (domain) return PROVIDERS.find((p) => p.match.test(domain)) ?? null
  const host = normaliseHost(value)
  if (host.endsWith('gmail.com') || host.endsWith('googlemail.com')) return PROVIDERS[0]
  if (host.endsWith('yahoo.com')) return PROVIDERS[1]
  if (host.endsWith('me.com') || host.endsWith('icloud.com')) return PROVIDERS[4]
  return null
}

// suggestServer proposes the server for a known provider from the user name.
export function suggestServer(username: string): string {
  return providerFor(username)?.server ?? ''
}

// providerHint tells what a known provider needs, from the user name or server.
export function providerHint(username: string, host: string): string {
  return providerFor(username)?.hint ?? providerFor(host)?.hint ?? ''
}

// ---- the file ----

const ACCEPTED = ['.mbox', '.mbx', '.eml', '.zip']

function extension(name: string): string {
  const lower = name.toLowerCase()
  if (lower.endsWith('.tar.gz')) return '.tar.gz'
  const dot = lower.lastIndexOf('.')
  return dot > 0 ? lower.slice(dot) : ''
}

// fileProblem says why a file can't be imported, or null. Files without an
// extension are allowed: mail programs like Thunderbird keep folders that way.
export function fileProblem(file: { name: string; size: number }): string | null {
  const ext = extension(file.name)
  if (ext === '.tgz' || ext === '.tar.gz' || ext === '.gz') {
    return 'Compressed files of this kind can’t be read. For Google Takeout, choose the .zip format when you export.'
  }
  if (ext && !ACCEPTED.includes(ext)) return `“${file.name}” isn’t mail. Choose a .mbox, .eml or .zip file.`
  if (file.size === 0) return `“${file.name}” is empty.`
  if (file.size > MAX_IMPORT_BYTES) return `“${file.name}” is too large. The limit is 20 GB.`
  return null
}

// ---- uploading in parts ----

export type UploadOptions = {
  size: number
  // Bytes the server has already (when an upload continues).
  start?: number
  partBytes?: number
  // Sends the bytes from offset to end; resolves with what the server has.
  sendPart: (offset: number, end: number, onBytes: (sent: number) => void) => Promise<number>
  // Asks the server how many bytes it has.
  received: () => Promise<number>
  onProgress: (done: number) => void
  // Failed attempts in a row before giving up.
  maxRetries?: number
  wait?: (ms: number) => Promise<void>
  signal?: AbortSignal
}

// retriable: a broken connection, a part the server didn't expect at that
// offset, or a server that is busy for a moment.
export function retriable(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false
  return [0, 408, 409, 500, 502, 503, 504].includes(error.status)
}

export function retryDelay(attempt: number): number {
  return Math.min(30_000, 1000 * 2 ** (attempt - 1))
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

// uploadInParts sends a file in parts. After an error it asks the server
// how far it got and goes on from there.
export async function uploadInParts(options: UploadOptions): Promise<void> {
  const { size, sendPart, received, onProgress, signal } = options
  const partBytes = Math.min(options.partBytes ?? PART_BYTES, MAX_PART_BYTES)
  const maxRetries = options.maxRetries ?? 6
  const wait = options.wait ?? sleep
  let offset = options.start ?? 0
  let failures = 0
  onProgress(offset)
  while (offset < size) {
    if (signal?.aborted) throw new DOMException('The upload was stopped.', 'AbortError')
    const end = Math.min(offset + partBytes, size)
    try {
      const from = offset
      const got = await sendPart(from, end, (sent) => onProgress(Math.min(from + sent, end)))
      if (got <= offset) throw new ApiError(409, 'The server did not take the part.')
      offset = got
      failures = 0
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') throw error
      if (!retriable(error)) throw error
      failures++
      if (failures > maxRetries) throw error
      await wait(retryDelay(failures))
      try {
        offset = await received()
      } catch (again) {
        if (!retriable(again)) throw again
      }
    }
    onProgress(offset)
  }
}

// ---- choosing folders ----

export function isJunkOrTrash(folder: Pick<ImportFolder, 'note'>): boolean {
  return folder.note === 'junk' || folder.note === 'trash'
}

export function canImport(folder: Pick<ImportFolder, 'local'>): boolean {
  return folder.local !== ''
}

// initialSelection: what the server chose, and for this panel's own export
// also Junk and Trash (they were cleaned up already once).
export function initialSelection(check: ImportCheck): string[] {
  return check.folders
    .filter((f) => canImport(f) && (f.selected || (check.own_export === true && isJunkOrTrash(f))))
    .map((f) => f.remote)
}

// withKind switches every folder with note on or off in the selection.
export function withKind(selected: string[], check: ImportCheck, note: string, on: boolean): string[] {
  const names = check.folders.filter((f) => f.note === note && canImport(f)).map((f) => f.remote)
  const rest = selected.filter((name) => !names.includes(name))
  return on ? check.folders.filter((f) => rest.includes(f.remote) || names.includes(f.remote)).map((f) => f.remote) : rest
}

export function kindSelected(selected: string[], check: ImportCheck, note: string): boolean {
  const names = check.folders.filter((f) => f.note === note && canImport(f)).map((f) => f.remote)
  return names.length > 0 && names.every((name) => selected.includes(name))
}

export function selectionTotals(check: ImportCheck, selected: string[]): { messages: number; bytes: number } {
  return check.folders
    .filter((f) => selected.includes(f.remote))
    .reduce((sum, f) => ({ messages: sum.messages + f.messages, bytes: sum.bytes + f.bytes }), { messages: 0, bytes: 0 })
}

export function fitsQuota(check: ImportCheck, bytes: number): boolean {
  return check.quota_left < 0 || bytes <= check.quota_left
}

export function quotaText(check: ImportCheck, bytes: number): string {
  if (check.quota_left < 0) return `${formatBytes(bytes)} to copy. The mailbox has no storage limit.`
  if (fitsQuota(check, bytes)) return `${formatBytes(bytes)} to copy, ${formatBytes(check.quota_left)} free in the mailbox.`
  return `${formatBytes(bytes)} to copy, but only ${formatBytes(check.quota_left)} is free in the mailbox. The import stops when it is full; untick some folders or ask for a larger mailbox.`
}

export function choicesFor(selected: string[]): ImportChoices {
  return { folders: selected }
}

const NOTES: Record<string, string> = {
  junk: 'Junk mail',
  trash: 'Deleted mail',
  starred: 'Only repeats mail from other folders',
  important: 'Only repeats mail from other folders',
  'all-mail': 'Copied last into Archive, without what other folders brought already',
  'too-many': 'Too many folders; not copied',
}

export function folderNote(folder: Pick<ImportFolder, 'note' | 'local' | 'remote'>): string {
  const note = folder.note ? (NOTES[folder.note] ?? '') : ''
  if (folder.local && folder.local !== folder.remote) return note ? `Into ${folder.local} · ${note}` : `Into ${folder.local}`
  return note
}

// ---- jobs ----

const ACTIVE = ['uploading', 'checking', 'waiting', 'running']

export function isActive(job: Pick<MailImport, 'state'>): boolean {
  return ACTIVE.includes(job.state)
}

export function isFinished(job: Pick<MailImport, 'state'>): boolean {
  return job.state === 'done' || job.state === 'failed' || job.state === 'cancelled'
}

export function needsPassword(job: Pick<MailImport, 'kind' | 'state'>): boolean {
  return job.kind === 'imap' && (job.state === 'paused' || job.state === 'interrupted')
}

export function jobSource(job: Pick<MailImport, 'kind' | 'host' | 'username' | 'file_name'>): string {
  if (job.kind === 'imap') return `${job.username ?? ''} on ${job.host ?? ''}`
  return job.file_name ?? 'a file'
}

type Tone = 'positive' | 'neutral' | 'warning'

export function jobState(job: Pick<MailImport, 'state' | 'kind'>): { label: string; tone: Tone; sentence: string } {
  switch (job.state) {
    case 'uploading':
      return { label: 'Uploading', tone: 'neutral', sentence: 'The file is being uploaded.' }
    case 'checking':
      return { label: 'Reading the file', tone: 'neutral', sentence: 'The file is being read to see what it holds.' }
    case 'ready':
      return { label: 'Ready', tone: 'neutral', sentence: 'The file is read. Choose the folders and start.' }
    case 'waiting':
      return { label: 'Waiting', tone: 'neutral', sentence: 'Starts as soon as other imports on this server are done.' }
    case 'running':
      return { label: 'Copying', tone: 'neutral', sentence: 'Mail is being copied.' }
    case 'paused':
      return {
        label: 'Paused',
        tone: 'warning',
        sentence: 'The other server limits how much may be downloaded per day. Continue tomorrow; it goes on where it stopped.',
      }
    case 'interrupted':
      return job.kind === 'imap'
        ? {
            label: 'Interrupted',
            tone: 'warning',
            sentence: 'The mail server restarted. Continue with the password; it goes on where it stopped.',
          }
        : { label: 'Interrupted', tone: 'warning', sentence: 'The mail server restarted; the import goes on by itself.' }
    case 'done':
      return { label: 'Done', tone: 'positive', sentence: 'All chosen mail was copied.' }
    case 'failed':
      return { label: 'Failed', tone: 'warning', sentence: 'The import stopped.' }
    case 'cancelled':
      return { label: 'Cancelled', tone: 'neutral', sentence: 'The import was cancelled. Mail copied until then stays.' }
  }
  return { label: job.state, tone: 'neutral', sentence: '' }
}

function count(n: number, one: string, many: string): string {
  return `${n.toLocaleString('en')} ${n === 1 ? one : many}`
}

export function jobCounts(job: Pick<MailImport, 'messages' | 'copied' | 'skipped' | 'failed' | 'bytes'>): string {
  const parts = [`${count(job.copied, 'message', 'messages')} copied (${formatBytes(job.bytes)})`]
  if (job.skipped) parts.push(`${job.skipped.toLocaleString('en')} already there`)
  if (job.failed) parts.push(`${job.failed.toLocaleString('en')} not copied`)
  if (job.messages) parts.push(`of ${job.messages.toLocaleString('en')}`)
  return parts.join(' · ')
}

// jobShare is how far a job got (0 to 100), or null when unknown.
export function jobShare(job: Pick<MailImport, 'state' | 'messages' | 'copied' | 'skipped' | 'failed' | 'received' | 'file_size'>): number | null {
  if (job.state === 'uploading') {
    return job.file_size ? Math.min(100, Math.round(((job.received ?? 0) / job.file_size) * 100)) : null
  }
  if (!job.messages) return null
  return Math.min(100, Math.round(((job.copied + job.skipped + job.failed) / job.messages) * 100))
}
