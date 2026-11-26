import { ApiError, encodeSegment, fallbackMessage, request } from '../../shared/api/index.ts'
import type {
  AppPassword,
  ApplyResult,
  ImportCheck,
  ImportChoices,
  ImportSource,
  MailImport,
  LimitsChange,
  MailAlias,
  MailDomain,
  Mailbox,
  MailboxChange,
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

export const listMailboxes = async (domain: string) =>
  (await request<Mailbox[] | null>(`${domainPath(domain)}/mailboxes`)) ?? []
export const createMailbox = (domain: string, localPart: string, password: string, displayName = '') =>
  request<Mailbox>(`${domainPath(domain)}/mailboxes`, {
    method: 'POST',
    body: displayName ? { local_part: localPart, password, display_name: displayName } : { local_part: localPart, password },
  })
export const setMailboxPassword = (id: number, password: string) =>
  request<void>(`/mail/mailboxes/${id}/password`, { method: 'POST', body: { password } })
export const setMailboxLimits = (id: number, change: LimitsChange) => updateMailbox(id, change)
export const updateMailbox = (id: number, change: MailboxChange) =>
  request<Mailbox>(`/mail/mailboxes/${id}`, { method: 'PATCH', body: change })

// ---- app passwords ----

const appPasswordsPath = (id: number) => `/mail/mailboxes/${id}/app-passwords`

export const listAppPasswords = async (id: number) => (await request<AppPassword[] | null>(appPasswordsPath(id))) ?? []
// The answer carries the password once; callers show it and drop it.
export const createAppPassword = (id: number, name: string) =>
  request<NewAppPassword>(appPasswordsPath(id), { method: 'POST', body: { name } })
export const revokeAppPassword = (id: number, appId: string) =>
  request<void>(`${appPasswordsPath(id)}/${encodeSegment(appId)}`, { method: 'DELETE' })
export const unblockMailbox = (id: number) => request<void>(`/mail/mailboxes/${id}/unblock`, { method: 'POST' })
export const deleteMailbox = (id: number) => request<void>(`/mail/mailboxes/${id}`, { method: 'DELETE' })
// A file download, opened as a link (the session cookie goes along).
export const appleProfileHref = (id: number) => `/api/mail/mailboxes/${id}/apple-profile`

export const listAliases = async (domain: string) =>
  ((await request<MailAlias[] | null>(`${domainPath(domain)}/aliases`)) ?? []).map((alias) => ({
    ...alias,
    targets: alias.targets ?? [],
  }))
export const createAlias = (domain: string, localPart: string, targets: string[]) =>
  request<MailAlias>(`${domainPath(domain)}/aliases`, { method: 'POST', body: { local_part: localPart, targets } })
export const updateAlias = (id: number, localPart: string, targets: string[]) =>
  request<MailAlias>(`/mail/aliases/${id}`, { method: 'PUT', body: { local_part: localPart, targets } })
export const deleteAlias = (id: number) => request<void>(`/mail/aliases/${id}`, { method: 'DELETE' })

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
