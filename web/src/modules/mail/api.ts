import { ApiError, encodeSegment, fallbackMessage, request } from '../../shared/api/index.ts'
import type {
  AppPassword,
  ApplyResult,
  ImportCheck,
  ImportChoices,
  ImportSource,
  MailImport,
  LimitsChange,
  AliasOptions,
  MailAlias,
  MailDomain,
  Mailbox,
  MailboxChange,
  MailboxKind,
  NewMailbox,
  MailboxRules,
  MailOrganisation,
  MailRecords,
  MailStatus,
  NewAppPassword,
} from './types.ts'

const domainPath = (domain: string) => `/mail/domains/${encodeSegment(domain)}`

// Lists may come as null from the server; the interface always sees arrays.
function withDomainLists(mail: MailDomain): MailDomain {
  const organisation = mail.organisation ?? null
  return { ...mail, organisation: organisation ? { ...organisation, domains: organisation.domains ?? [] } : null }
}

export const listMailDomains = async () =>
  ((await request<MailDomain[] | null>('/mail/domains')) ?? []).map(withDomainLists)
export const getMailDomain = async (domain: string) => withDomainLists(await request<MailDomain>(domainPath(domain)))
export const enableMailDomain = (domain: string) =>
  request<MailDomain>('/mail/domains', { method: 'POST', body: { domain } })
export const checkMailDomain = (domain: string) =>
  request<MailDomain>(`${domainPath(domain)}/check`, { method: 'POST' })
export const activateMailDomain = (domain: string) =>
  request<MailDomain>(`${domainPath(domain)}/activate`, { method: 'POST' })
export const deleteMailDomain = (domain: string, confirm: string) =>
  request<void>(domainPath(domain), { method: 'DELETE', body: { confirm } })

export const getMailStatus = () => request<MailStatus>('/mail/status')

// ---- organisations ----

const organisationPath = (id: string) => `/mail/organisations/${encodeSegment(id)}`

export const listOrganisations = async () =>
  ((await request<MailOrganisation[] | null>('/mail/organisations')) ?? []).map((organisation) => ({
    ...organisation,
    domains: organisation.domains ?? [],
  }))
export const createOrganisation = (name: string) =>
  request<MailOrganisation>('/mail/organisations', { method: 'POST', body: { name } })
export const renameOrganisation = (id: string, name: string) =>
  request<MailOrganisation>(organisationPath(id), { method: 'PATCH', body: { name } })
export const deleteOrganisation = (id: string) => request<void>(organisationPath(id), { method: 'DELETE' })
// organisation "" lets the domain stand alone.
export const setDomainOrganisation = async (domain: string, organisation: string) =>
  withDomainLists(
    await request<MailDomain>(`${domainPath(domain)}/organisation`, { method: 'PUT', body: { organisation } }),
  )

// Lists may come as null (or, from an older server, not at all); the interface always sees them.
function withMailboxLists(mailbox: Mailbox): Mailbox {
  return {
    ...mailbox,
    kind: mailbox.kind === 'shared' ? 'shared' : 'personal',
    keep_sent_copy: mailbox.keep_sent_copy ?? false,
    shared_with: (mailbox.shared_with ?? []).map((entry) => ({ ...entry, rights: entry.rights ?? [] })),
    access_to: (mailbox.access_to ?? []).map((entry) => ({ ...entry, rights: entry.rights ?? [] })),
  }
}

const mailboxIdPath = (id: string) => `/mail/mailboxes/${encodeSegment(id)}`

export const listMailboxes = async (domain: string) =>
  ((await request<Mailbox[] | null>(`${domainPath(domain)}/mailboxes`)) ?? []).map(withMailboxLists)
export const getMailbox = async (id: string) => withMailboxLists(await request<Mailbox>(mailboxIdPath(id)))
// A shared mailbox has no password; empty fields stay out of the request.
export const createMailbox = async (domain: string, mailbox: NewMailbox) => {
  const body: Record<string, unknown> = { local_part: mailbox.local_part, kind: mailbox.kind }
  if (mailbox.kind === 'personal') body.password = mailbox.password ?? ''
  if (mailbox.display_name) body.display_name = mailbox.display_name
  if (mailbox.kind === 'shared' && mailbox.keep_sent_copy) body.keep_sent_copy = true
  return withMailboxLists(await request<Mailbox>(`${domainPath(domain)}/mailboxes`, { method: 'POST', body }))
}
// Making a mailbox personal needs a new password; making it shared drops its password and app passwords.
export const setMailboxKind = async (id: string, kind: MailboxKind, password = '') =>
  withMailboxLists(
    await request<Mailbox>(`${mailboxIdPath(id)}/kind`, {
      method: 'PUT',
      body: kind === 'personal' ? { kind, password } : { kind },
    }),
  )
export const setKeepSentCopy = async (id: string, keep: boolean) =>
  withMailboxLists(
    await request<Mailbox>(`${mailboxIdPath(id)}/keep-sent-copy`, { method: 'PUT', body: { keep_sent_copy: keep } }),
  )
// Gives a personal mailbox of the organisation access to a shared one, or changes its rights.
export const setMailboxGrant = async (id: string, address: string, rights: string[]) =>
  withMailboxLists(await request<Mailbox>(`${mailboxIdPath(id)}/grants`, { method: 'PUT', body: { address, rights } }))
export const removeMailboxGrant = async (id: string, granteeId: string) =>
  withMailboxLists(await request<Mailbox>(`${mailboxIdPath(id)}/grants/${encodeSegment(granteeId)}`, { method: 'DELETE' }))
export const setMailboxPassword = (id: string, password: string) =>
  request<void>(`/mail/mailboxes/${encodeSegment(id)}/password`, { method: 'POST', body: { password } })
export const setMailboxLimits = (id: string, change: LimitsChange) => updateMailbox(id, change)
export const updateMailbox = (id: string, change: MailboxChange) =>
  request<Mailbox>(`/mail/mailboxes/${encodeSegment(id)}`, { method: 'PATCH', body: change })

// ---- app passwords ----

const appPasswordsPath = (id: string) => `/mail/mailboxes/${encodeSegment(id)}/app-passwords`

export const listAppPasswords = async (id: string) => (await request<AppPassword[] | null>(appPasswordsPath(id))) ?? []
// The answer carries the password once; callers show it and drop it.
export const createAppPassword = (id: string, name: string) =>
  request<NewAppPassword>(appPasswordsPath(id), { method: 'POST', body: { name } })
export const revokeAppPassword = (id: string, appId: string) =>
  request<void>(`${appPasswordsPath(id)}/${encodeSegment(appId)}`, { method: 'DELETE' })
// ---- out-of-office and forwarding ----

const mailboxRulesPath = (id: string) => `/mail/mailboxes/${encodeSegment(id)}`

export type OutOfOfficeRequest = {
  enabled: boolean
  subject: string
  text: string
  start: string
  end: string
  zone: string
  days: number
}
export type ForwardingRequest = { enabled: boolean; to: string; keep_copy: boolean }

export const getMailboxRules = (id: string) => request<MailboxRules>(`${mailboxRulesPath(id)}/rules`)
export const setMailboxOutOfOffice = (id: string, body: OutOfOfficeRequest, replace: boolean) =>
  request<MailboxRules>(`${mailboxRulesPath(id)}/out-of-office`, { method: 'PUT', body: { ...body, replace } })
export const setMailboxForwarding = (id: string, body: ForwardingRequest, replace: boolean) =>
  request<MailboxRules>(`${mailboxRulesPath(id)}/forwarding`, { method: 'PUT', body: { ...body, replace } })
export const setMailboxMayForward = (id: string, allowed: boolean) =>
  request<void>(`${mailboxRulesPath(id)}/may-forward`, { method: 'PUT', body: { allowed } })

export const unblockMailbox = (id: string) => request<void>(`/mail/mailboxes/${encodeSegment(id)}/unblock`, { method: 'POST' })
export const deleteMailbox = (id: string) => request<void>(`/mail/mailboxes/${encodeSegment(id)}`, { method: 'DELETE' })
// A file download, opened as a link (the session cookie goes along).
export const appleProfileHref = (id: string) => `/api/mail/mailboxes/${encodeSegment(id)}/apple-profile`

export const listAliases = async (domain: string) =>
  ((await request<MailAlias[] | null>(`${domainPath(domain)}/aliases`)) ?? []).map((alias) => ({
    ...alias,
    targets: alias.targets ?? [],
    sender_policy: alias.sender_policy || 'anyone',
    hide_from_directory: alias.hide_from_directory ?? false,
  }))
export const createAlias = (domain: string, localPart: string, targets: string[], options: AliasOptions) =>
  request<MailAlias>(`${domainPath(domain)}/aliases`, {
    method: 'POST',
    body: { local_part: localPart, targets, ...options },
  })
export const updateAlias = (id: string, localPart: string, targets: string[], options: AliasOptions) =>
  request<MailAlias>(`/mail/aliases/${encodeSegment(id)}`, { method: 'PUT', body: { local_part: localPart, targets, ...options } })
export const deleteAlias = (id: string) => request<void>(`/mail/aliases/${encodeSegment(id)}`, { method: 'DELETE' })

export async function getMailRecords(domain: string): Promise<MailRecords> {
  const data = await request<MailRecords>(`${domainPath(domain)}/records`)
  return {
    ...data,
    records: (data.records ?? []).map((record) => ({ ...record, found: record.found ?? [] })),
  }
}

export async function applyMailRecords(domain: string, confirm: boolean): Promise<ApplyResult> {
  const data = await request<ApplyResult>(`${domainPath(domain)}/records`, { method: 'POST', body: { confirm } })
  return {
    applied: data.applied,
    changes: (data.changes ?? []).map((change) => ({ ...change, old: change.old ?? [], new: change.new ?? [] })),
  }
}

// ---- import and export ----

const mailboxPath = (domain: string, localPart: string) =>
  `${domainPath(domain)}/mailboxes/${encodeSegment(localPart)}`

export const checkImport = (domain: string, localPart: string, source: ImportSource) =>
  request<ImportCheck>(`${mailboxPath(domain, localPart)}/imports/check`, { method: 'POST', body: source })
export const startImport = (domain: string, localPart: string, source: ImportSource, choices: ImportChoices) =>
  request<MailImport>(`${mailboxPath(domain, localPart)}/imports`, { method: 'POST', body: { ...source, choices } })
export const createUpload = (domain: string, localPart: string, name: string, size: number) =>
  request<MailImport>(`${mailboxPath(domain, localPart)}/uploads`, { method: 'POST', body: { name, size } })
export const startFileImport = (domain: string, localPart: string, id: number, choices: ImportChoices) =>
  request<MailImport>(`${mailboxPath(domain, localPart)}/imports/${id}/start`, { method: 'POST', body: { choices } })
export const continueImport = (domain: string, localPart: string, id: number, password: string) =>
  request<MailImport>(`${mailboxPath(domain, localPart)}/imports/${id}/start`, { method: 'POST', body: { password } })
export const listImports = async (domain: string, localPart: string) =>
  (await request<MailImport[] | null>(`${mailboxPath(domain, localPart)}/imports`)) ?? []
export const getImport = (domain: string, localPart: string, id: number) =>
  request<MailImport>(`${mailboxPath(domain, localPart)}/imports/${id}`)
export const cancelImport = (domain: string, localPart: string, id: number) =>
  request<void>(`${mailboxPath(domain, localPart)}/imports/${id}`, { method: 'DELETE' })
// A file download, opened as a link (the session cookie goes along).
export const exportHref = (domain: string, localPart: string) => `/api${mailboxPath(domain, localPart)}/export`

// uploadPart sends one part of a file and reports the bytes sent so far;
// it resolves with how many bytes the server has stored.
export function uploadPart(
  domain: string,
  localPart: string,
  id: number,
  offset: number,
  part: Blob,
  onBytes: (sent: number) => void,
  signal?: AbortSignal,
): Promise<number> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('PUT', `/api${mailboxPath(domain, localPart)}/uploads/${id}?offset=${offset}`)
    xhr.setRequestHeader('X-Ownpanel-Request', '1')
    xhr.setRequestHeader('Content-Type', 'application/octet-stream')
    xhr.withCredentials = true
    xhr.upload.onprogress = (event) => onBytes(event.loaded)
    xhr.onerror = () => reject(new ApiError(0, "Can't reach the panel. Check your connection and try again."))
    xhr.ontimeout = xhr.onerror
    xhr.onabort = () => reject(new DOMException('The upload was stopped.', 'AbortError'))
    xhr.onload = () => {
      let data: unknown = null
      try {
        data = JSON.parse(xhr.responseText)
      } catch {
        // Not JSON; handled below.
      }
      if (xhr.status >= 200 && xhr.status < 300 && data && typeof data === 'object' && 'received' in data) {
        resolve(Number(data.received))
        return
      }
      const message =
        data && typeof data === 'object' && 'error' in data && typeof data.error === 'string' ? data.error : ''
      reject(new ApiError(xhr.status, message || fallbackMessage(xhr.status)))
    }
    signal?.addEventListener('abort', () => xhr.abort())
    xhr.send(part)
  })
}
