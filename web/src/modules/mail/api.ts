import { encodeSegment, request } from '../../shared/api/index.ts'
import type {
  ApplyResult,
  LimitsChange,
  MailAlias,
  MailDomain,
  Mailbox,
  MailRecords,
  MailStatus,
} from './types.ts'

const domainPath = (domain: string) => `/mail/domains/${encodeSegment(domain)}`

export const listMailDomains = async () => (await request<MailDomain[] | null>('/mail/domains')) ?? []
export const getMailDomain = (domain: string) => request<MailDomain>(domainPath(domain))
export const enableMailDomain = (domain: string) =>
  request<MailDomain>('/mail/domains', { method: 'POST', body: { domain } })
export const checkMailDomain = (domain: string) =>
  request<MailDomain>(`${domainPath(domain)}/check`, { method: 'POST' })
export const activateMailDomain = (domain: string) =>
  request<MailDomain>(`${domainPath(domain)}/activate`, { method: 'POST' })
export const deleteMailDomain = (domain: string, confirm: string) =>
  request<void>(domainPath(domain), { method: 'DELETE', body: { confirm } })

export const getMailStatus = () => request<MailStatus>('/mail/status')

export const listMailboxes = async (domain: string) =>
  (await request<Mailbox[] | null>(`${domainPath(domain)}/mailboxes`)) ?? []
export const createMailbox = (domain: string, localPart: string, password: string) =>
  request<Mailbox>(`${domainPath(domain)}/mailboxes`, { method: 'POST', body: { local_part: localPart, password } })
export const setMailboxPassword = (id: number, password: string) =>
  request<void>(`/mail/mailboxes/${id}/password`, { method: 'POST', body: { password } })
export const setMailboxLimits = (id: number, change: LimitsChange) =>
  request<Mailbox>(`/mail/mailboxes/${id}`, { method: 'PATCH', body: change })
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
