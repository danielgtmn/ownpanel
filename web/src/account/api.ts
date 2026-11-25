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
  // Where calendar and contacts programs connect.
  dav_url?: string
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
  // Who the person shared their own mailbox with.
  shared_with: Delegate[]
  // Mailboxes the person may use besides their own.
  access: MailboxAccess[]
  // Only on the answer to a password change: the password was changed, but not all that came with it.
  warning?: string
}

// Rights are "read", "write", "manage", "send-as", "send-on-behalf" (src/shared/sharing).
export type Delegate = { address: string; display_name: string; rights: string[] }

export type MailboxAccess = {
  address: string
  display_name: string
  // A shared mailbox (rather than someone's own mailbox they shared).
  shared: boolean
  rights: string[]
  // Where mail programs show its folders, like "Shared/info@example.com".
  folder: string
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

// Lists may come as null (or, from an older server, not at all); the page always sees arrays.
export function withLists<T extends Me>(me: T): T {
  return {
    ...me,
    app_passwords: me.app_passwords ?? [],
    shared_with: (me.shared_with ?? []).map((entry) => ({ ...entry, rights: entry.rights ?? [] })),
    access: (me.access ?? []).map((entry) => ({ ...entry, rights: entry.rights ?? [] })),
  }
}

const account = async (path: string, options: Options = {}) => withLists(await request<Me>(path, options))

// Wrong address or password answers 401 too; that is not an ended session.
export const signIn = (address: string, password: string) =>
  account('/session', { method: 'POST', body: { address, password }, quiet401: true })

export const signOut = () => request<void>('/session', { method: 'DELETE', quiet401: true })

export const loadMe = (quiet401 = false) => account('/me', { quiet401 })

export const changePassword = (current: string, next: string, revokeAppPasswords: boolean) =>
  account('/password', {
    method: 'PUT',
    body: { current_password: current, new_password: next, revoke_app_passwords: revokeAppPasswords },
  })

export const setDisplayName = (name: string) =>
  account('/display-name', { method: 'PUT', body: { display_name: name } })

export const setAppPasswordsOnly = (on: boolean) =>
  account('/app-passwords-only', { method: 'PUT', body: { app_passwords_only: on } })

export const createAppPassword = (name: string) =>
  request<NewAppPassword>('/app-passwords', { method: 'POST', body: { name } })

export const revokeAppPassword = (id: string) =>
  request<void>(`/app-passwords/${encodeURIComponent(id)}`, { method: 'DELETE' })

// Replaces the whole list of people the person's mailbox is shared with; empty: nobody.
export const setDelegations = (grants: { address: string; rights: string[] }[]) =>
  account('/delegations', { method: 'PUT', body: { grants } })

export const APPLE_SETUP_URL = `${BASE_PATH}/apple-setup`

export type CollectionKind = 'calendar' | 'addressbook'

export type Collection = {
  kind: CollectionKind
  name: string
  display_name: string
  color: string
  objects: number
  bytes: number
}

export type Collections = { collections: Collection[]; max_file_bytes: number }

export type ImportResult = { added: number; replaced: number; skipped: number }

export const loadCollections = () => request<Collections>('/collections')

function collectionPath(collection: Pick<Collection, 'kind' | 'name'>): string {
  return `/collections/${collection.kind}/${encodeURIComponent(collection.name)}`
}

export function exportURL(collection: Pick<Collection, 'kind' | 'name'>): string {
  return BASE_PATH + collectionPath(collection) + '/export'
}

// Uploads a calendar (.ics) or contacts (.vcf) file into a collection. The body is the file itself.
export async function importFile(collection: Pick<Collection, 'kind' | 'name'>, file: Blob): Promise<ImportResult> {
  let response: Response
  try {
    response = await fetch(BASE_PATH + collectionPath(collection) + '/import', {
      method: 'POST',
      headers: {
        'X-Ownpanel-Request': '1',
        'Content-Type': collection.kind === 'calendar' ? 'text/calendar' : 'text/vcard',
      },
      body: file,
      credentials: 'same-origin',
    })
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
    if (response.status === 401) sessionEnded?.()
    throw new AccountError(response.status, message.trim() || fallback(response.status))
  }
  return (await response.json()) as ImportResult
}

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

function filterLists(filters: Filters): Filters {
  return {
    ...filters,
    folders: filters.folders ?? [],
    rules: { ...filters.rules, filters: filters.rules.filters ?? [] },
  }
}

async function saveFilters(path: string, body: unknown): Promise<Filters> {
  try {
    return filterLists(await request<Filters>(path, { method: 'PUT', body }))
  } catch (error) {
    if (error instanceof AccountError && error.status === 409) {
      throw new AccountError(409, REPLACES_MAIL_PROGRAM_FILTERS)
    }
    throw error
  }
}

export const loadFilters = async () => filterLists(await request<Filters>('/filters'))

export const setOutOfOffice = (outOfOffice: OutOfOfficeInput, replace: boolean) =>
  saveFilters('/out-of-office', replace ? { out_of_office: outOfOffice, replace } : { out_of_office: outOfOffice })

export const setForwarding = (forwarding: ForwardingInput, replace: boolean) =>
  saveFilters('/forwarding', replace ? { forwarding, replace } : { forwarding })

export const setFilterRules = (filters: FilterRule[], replace: boolean) =>
  saveFilters('/filters', replace ? { filters, replace } : { filters })
