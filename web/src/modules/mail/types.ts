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
}

export type MailAlias = {
  id: number
  address: string
  domain: string
  local_part: string
  catch_all: boolean
  targets: string[]
  owner: string
  created_at: string
}

export type MailClientSettings = {
  server: string
  imap_port: number
  smtp_port: number
  smtp_starttls_port: number
  user_name_is_address: boolean
}

export type MailStatus = {
  mail_host: string
  service_set_up: boolean
  reachable: boolean
  in_sync: boolean
  problem: string
  client: MailClientSettings
}

export type MailRecordKind = 'mx' | 'spf' | 'dkim' | 'dmarc' | 'srv-submissions' | 'srv-imaps'
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
}

export type ApplyResult = { changes: RecordChange[]; applied: boolean }

export type LimitsChange = { quota_bytes?: number; send_per_hour?: number; send_per_day?: number }
