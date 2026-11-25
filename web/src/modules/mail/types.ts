// Shapes of /api/mail (see internal/panel/mail.go, mailboxes.go, mailsync.go and wiki/API.md).

export type MailDomain = {
  domain: string
  owner: string
  active: boolean
  activated_at: string | null
  checked_at: string | null
  problem: string
  mailboxes: number
  aliases: number
  dkim_selector: string
  dkim_record: string
  created_at: string
  // The group of domains whose mailboxes may share with each other; null: the domain stands alone.
  organisation: MailDomainOrganisation | null
}

export type MailDomainOrganisation = {
  id: string
  name: string
  // false: an administrator put the domain into someone else's organisation.
  own: boolean
  // The organisation's other domains this person may see.
  domains: string[]
}

export type MailOrganisation = {
  id: string
  name: string
  owner: string
  domains: string[]
  created_at: string
}

export type MailboxUsage = {
  used_bytes: number
  messages: number
  blocked: boolean
  blocked_reason: string
  blocked_at: string | null
}

export type Mailbox = {
  id: number
  address: string
  domain: string
  local_part: string
  owner: string
  quota_bytes: number
  send_per_hour: number
  send_per_day: number
  created_at: string
  usage: MailboxUsage | null
  // The sender's name shown with the address; "" when none is set.
  display_name: string
  // Mail programs can sign in only with app passwords, not with the mailbox's own password.
  app_passwords_only: boolean
  // shared: nobody signs in to it; personal mailboxes of the organisation get access to it.
  kind: MailboxKind
  // Shared only: mail sent for it is also kept in its Sent folder.
  keep_sent_copy: boolean
  // Mailboxes that have access to this one.
  shared_with: MailboxAccessor[]
  // Mailboxes this one has access to.
  access_to: MailboxAccess[]
}

export type MailboxKind = 'personal' | 'shared'

// Rights are "read", "write", "manage", "send-as", "send-on-behalf" (src/shared/sharing).
// active false: the two mailboxes are no longer in the same organisation, so the access doesn't count.
export type MailboxAccessor = {
  mailbox_id: number
  address: string
  display_name: string
  rights: string[]
  active: boolean
}

export type MailboxAccess = {
  mailbox_id: number
  address: string
  kind: MailboxKind
  rights: string[]
  active: boolean
}

export type NewMailbox = {
  local_part: string
  kind: MailboxKind
  // Personal only.
  password?: string
  display_name?: string
  // Shared only.
  keep_sent_copy?: boolean
}

export type AppPassword = {
  id: string
  name: string
  created_at: string
  last_used_at: string | null
  // The network of the last sign-in, like 203.0.113.0/24.
  last_used_network: string
}

// Only the answer to creating one carries the password, and only once.
export type NewAppPassword = AppPassword & { password: string }

export type MailAlias = {
  id: number
  address: string
  domain: string
  local_part: string
  catch_all: boolean
  targets: string[]
  owner: string
  created_at: string
  // Who may send mail to the address; the catch-all only allows "anyone".
  sender_policy: SenderPolicy
  // Left out of the organisation's address book.
  hide_from_directory: boolean
}

export type SenderPolicy = 'anyone' | 'organisation' | 'members'

export type AliasOptions = { sender_policy: SenderPolicy; hide_from_directory: boolean }

export type MailClientSettings = {
  server: string
  imap_port: number
  smtp_port: number
  smtp_starttls_port: number
  user_name_is_address: boolean
  // Where calendar and contacts programs connect; missing when the server offers no calendars and contacts.
  dav_url?: string
}

export type MailStatus = {
  mail_host: string
  service_set_up: boolean
  reachable: boolean
  in_sync: boolean
  problem: string
  client: MailClientSettings
}

export type MailRecordKind =
  | 'mx'
  | 'spf'
  | 'dkim'
  | 'dmarc'
  | 'srv-submissions'
  | 'srv-imaps'
  | 'autoconfig'
  | 'autodiscover'
  | 'srv-caldav'
  | 'txt-caldav'
  | 'srv-carddav'
  | 'txt-carddav'
export type MailRecordStatus = 'ok' | 'missing' | 'different' | 'conflict' | 'unknown'

export type MailRecord = {
  kind: MailRecordKind | string
  type: string
  name: string
  fqdn: string
  want: string
  found: string[]
  status: MailRecordStatus | string
  note: string
  // Recommended only: mail works without it (lets mail programs set themselves up).
  optional?: boolean
}

export type MailRecords = {
  domain: string
  mail_host: string
  records: MailRecord[]
  can_apply: boolean
  // Why the panel cannot set the records up itself; empty when it can.
  apply_note?: string
  connection_id: number | null
  zone: string
}

export type RecordAction = 'create' | 'replace' | 'add' | 'keep' | 'skip'

export type RecordChange = {
  kind: MailRecordKind | string
  type: string
  name: string
  action: RecordAction | string
  old: string[]
  new: string[]
  needs_confirm: boolean
  note: string
  optional?: boolean
}

export type ApplyResult = { changes: RecordChange[]; applied: boolean }

export type LimitsChange = { quota_bytes?: number; send_per_hour?: number; send_per_day?: number }

export type MailboxChange = LimitsChange & { display_name?: string; app_passwords_only?: boolean }

// Import and export of a mailbox (internal/mail/config/api.go, internal/panel/mailtransfer.go).

export type ImportState =
  | 'uploading'
  | 'checking'
  | 'ready'
  | 'waiting'
  | 'running'
  | 'paused'
  | 'done'
  | 'failed'
  | 'cancelled'
  | 'interrupted'

export type ImportNote = 'junk' | 'trash' | 'starred' | 'important' | 'all-mail' | 'too-many'

export type ImportFolder = {
  remote: string
  local: string
  special_use?: string
  messages: number
  bytes: number
  selected: boolean
  note?: ImportNote | string
}

export type ImportCheck = {
  folders: ImportFolder[]
  messages: number
  bytes: number
  quota_bytes: number
  used_bytes: number
  // -1: no limit.
  quota_left: number
  skipped_entries?: number
  // The file is a "Download all mail" file of this panel.
  own_export?: boolean
}

export type ImportChoices = { folders?: string[]; include_junk?: boolean; include_trash?: boolean }

export type ImportFolderProgress = {
  remote: string
  local: string
  copied: number
  skipped: number
  failed: number
  done?: boolean
}

export type ImportFailure = { folder: string; date?: string; subject?: string; reason: string }

export type MailImport = {
  id: number
  address: string
  kind: 'imap' | 'file'
  state: ImportState | string
  error?: string
  host?: string
  port?: number
  username?: string
  file_name?: string
  file_size?: number
  received?: number
  check?: ImportCheck
  choices: ImportChoices
  folders: ImportFolderProgress[] | null
  messages: number
  copied: number
  skipped: number
  failed: number
  bytes: number
  created: string
  started?: string
  finished?: string
  failures?: ImportFailure[]
}

export type ImportSource = { host: string; port: number; username: string; password: string }
