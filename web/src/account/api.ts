// The mailbox's own API on the mail server's name (/account/api/ of the mail
// service). It never touches the panel's API or session.

const BASE_PATH = '/account/api'

export type AppPassword = {
  id: string
  name: string
  created_at: string
  last_used_at: string | null
  last_used_network: string
}

export type ClientSettings = {
  server: string
  imap_port: number
  smtp_port: number
  smtp_starttls_port: number
  user_name_is_address: boolean
}

export type Me = {
  address: string
  display_name: string
  used_bytes: number
  messages: number
  quota_bytes: number
  app_passwords: AppPassword[]
  app_passwords_only: boolean
  app_passwords_available: boolean
  uses_known_days: number
  client: ClientSettings
  // Only on the answer to a password change: the password was changed, but not all that came with it.
  warning?: string
}

export type NewAppPassword = { app_password: AppPassword; password: string }

export class AccountError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'AccountError'
    this.status = status
  }
}

export const SESSION_ENDED = 'Your session has ended. Sign in again.'
const OFFLINE = "Can't reach the mail server. Check your connection and try again."

function fallback(status: number): string {
  if (status === 401) return SESSION_ENDED
  if (status === 429) return 'Too many attempts. Wait a moment and try again.'
  if (status >= 500) return 'Something went wrong on the server. Try again in a moment.'
  return `The request didn't go through (status ${status}).`
}

let sessionEnded: (() => void) | null = null

// Called when a request finds the session gone (expired, signed out elsewhere, password changed).
export function onSessionEnded(handler: (() => void) | null) {
  sessionEnded = handler
}

type Options = { method?: string; body?: unknown; quiet401?: boolean }

export async function request<T>(path: string, options: Options = {}): Promise<T> {
  const method = options.method ?? 'GET'
  const headers: Record<string, string> = {}
  if (method !== 'GET') headers['X-Ownpanel-Request'] = '1'
  let body: string | undefined
  if (options.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(options.body)
  }
  let response: Response
  try {
    response = await fetch(BASE_PATH + path, { method, headers, body, credentials: 'same-origin' })
  } catch {
    throw new AccountError(0, OFFLINE)
  }
  if (!response.ok) {
    let message = ''
    try {
      const data: unknown = await response.json()
      if (data && typeof data === 'object' && 'error' in data && typeof data.error === 'string') message = data.error
    } catch {
      // Not JSON: use the fallback.
    }
    if (response.status === 401 && !options.quiet401) sessionEnded?.()
    throw new AccountError(response.status, message.trim() || fallback(response.status))
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

export function accountErrorMessage(error: unknown): string {
  return error instanceof AccountError ? error.message : 'Something went wrong. Try again.'
}

export function isSessionEnded(error: unknown): boolean {
  return error instanceof AccountError && error.status === 401
}

// Wrong address or password answers 401 too; that is not an ended session.
export const signIn = (address: string, password: string) =>
  request<Me>('/session', { method: 'POST', body: { address, password }, quiet401: true })

export const signOut = () => request<void>('/session', { method: 'DELETE', quiet401: true })

export const loadMe = (quiet401 = false) => request<Me>('/me', { quiet401 })

export const changePassword = (current: string, next: string, revokeAppPasswords: boolean) =>
  request<Me>('/password', {
    method: 'PUT',
    body: { current_password: current, new_password: next, revoke_app_passwords: revokeAppPasswords },
  })

export const setDisplayName = (name: string) =>
  request<Me>('/display-name', { method: 'PUT', body: { display_name: name } })

export const setAppPasswordsOnly = (on: boolean) =>
  request<Me>('/app-passwords-only', { method: 'PUT', body: { app_passwords_only: on } })

export const createAppPassword = (name: string) =>
  request<NewAppPassword>('/app-passwords', { method: 'POST', body: { name } })

export const revokeAppPassword = (id: string) =>
  request<void>(`/app-passwords/${encodeURIComponent(id)}`, { method: 'DELETE' })

export const APPLE_SETUP_URL = `${BASE_PATH}/apple-setup`

// ---- filters and out-of-office (internal/mail/account/filters.go) ----

export type FilterField = 'from' | 'to' | 'subject' | 'list'
export type FilterOp = 'contains' | 'is'

export type FilterCondition = { field: FilterField; op: FilterOp; value: string }

export type FilterRule = {
  name: string
  match_any: boolean
  conditions: FilterCondition[]
  folder?: string
  mark_read?: boolean
  forward_to?: string
  delete?: boolean
}

export type OutOfOffice = {
  enabled: boolean
  subject: string
  text: string
  start?: string
  end?: string
  zone?: string
  days: number
  // Who set it for the person from the panel, and when; absent when the person did.
  set_by?: string
  set_at?: string
}

export type Forwarding = { enabled: boolean; to: string; keep_copy: boolean; set_by?: string; set_at?: string }

export type MailRules = {
  // false: filters written in a mail program are in effect; saving here replaces them.
  managed: boolean
  out_of_office: OutOfOffice
  forwarding: Forwarding
  filters: FilterRule[] | null
  active_script: string
  script?: string
}

export type Filters = { rules: MailRules; folders: string[] | null; may_forward: boolean }

export type OutOfOfficeInput = Pick<OutOfOffice, 'enabled' | 'subject' | 'text' | 'days'> & {
  start: string
  end: string
  zone: string
}
export type ForwardingInput = Pick<Forwarding, 'enabled' | 'to' | 'keep_copy'>

export const REPLACES_MAIL_PROGRAM_FILTERS =
  'Your filters were written in a mail program. Saving here replaces them.'

function withLists(filters: Filters): Filters {
  return {
    ...filters,
    folders: filters.folders ?? [],
    rules: { ...filters.rules, filters: filters.rules.filters ?? [] },
  }
}

async function saveFilters(path: string, body: unknown): Promise<Filters> {
  try {
    return withLists(await request<Filters>(path, { method: 'PUT', body }))
  } catch (error) {
    if (error instanceof AccountError && error.status === 409) {
      throw new AccountError(409, REPLACES_MAIL_PROGRAM_FILTERS)
    }
    throw error
  }
}

export const loadFilters = async () => withLists(await request<Filters>('/filters'))

export const setOutOfOffice = (outOfOffice: OutOfOfficeInput, replace: boolean) =>
  saveFilters('/out-of-office', replace ? { out_of_office: outOfOffice, replace } : { out_of_office: outOfOffice })

export const setForwarding = (forwarding: ForwardingInput, replace: boolean) =>
  saveFilters('/forwarding', replace ? { forwarding, replace } : { forwarding })

export const setFilterRules = (filters: FilterRule[], replace: boolean) =>
  saveFilters('/filters', replace ? { filters, replace } : { filters })
