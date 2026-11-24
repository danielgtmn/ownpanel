import type { RecordSet } from './types.ts'

export const RECORD_TYPES = ['A', 'AAAA', 'CNAME', 'TXT', 'MX', 'CAA', 'SRV', 'NS'] as const
export type RecordType = (typeof RECORD_TYPES)[number]

export const TTL_MIN = 60
export const TTL_MAX = 86400

const SORT_ORDER = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'CAA', 'SRV', 'NS']
const TYPE_ORDER: Record<string, number> = Object.fromEntries(SORT_ORDER.map((type, index) => [type, index]))

// The domain's own SOA and its NS records belong to the DNS provider's delegation.
export function isProviderManaged(set: Pick<RecordSet, 'name' | 'type'>): boolean {
  return set.type === 'SOA' || (set.type === 'NS' && set.name === '@')
}

export function isEditableRecordSet(set: RecordSet, zoneReadOnly: boolean): boolean {
  return !zoneReadOnly && !set.locked && !isProviderManaged(set)
}

export function compareRecordSets(a: RecordSet, b: RecordSet): number {
  const managedA = isProviderManaged(a)
  const managedB = isProviderManaged(b)
  if (managedA !== managedB) return managedA ? 1 : -1
  if (a.name === '@' && b.name !== '@') return -1
  if (b.name === '@' && a.name !== '@') return 1
  const byName = a.name.localeCompare(b.name, 'en', { sensitivity: 'base' })
  if (byName !== 0) return byName
  const rankA = TYPE_ORDER[a.type] ?? SORT_ORDER.length
  const rankB = TYPE_ORDER[b.type] ?? SORT_ORDER.length
  return rankA - rankB || a.type.localeCompare(b.type)
}

export function sortRecordSets(sets: RecordSet[]): RecordSet[] {
  return [...sets].sort(compareRecordSets)
}

export function normalizeName(input: string): string {
  return input.trim().toLowerCase()
}

export function fullName(name: string, zone: string): string {
  const relative = normalizeName(name)
  if (relative === '' || relative === '@') return zone
  return `${relative}.${zone}`
}

export function ttlLabel(ttl: number | null): string {
  return ttl === null ? 'Default' : `${ttl} seconds`
}

export function setKey(set: Pick<RecordSet, 'name' | 'type'>): string {
  return `${set.type}:${set.name}`
}

export function typeHint(type: string): string {
  switch (type) {
    case 'A':
      return 'An IPv4 address, like 203.0.113.10.'
    case 'AAAA':
      return 'An IPv6 address, like 2001:db8::10.'
    case 'CNAME':
      return 'The name this one is an alias for, like target.example.com. Only one value, and not on the domain itself.'
    case 'TXT':
      return 'Plain text without quotes, like v=spf1 include:_spf.example.com ~all.'
    case 'MX':
      return 'A priority and a mail server, like 10 mail.example.com. A lower number is tried first.'
    case 'CAA':
      return 'Flag, tag and value, like 0 issue "letsencrypt.org".'
    case 'SRV':
      return 'Priority, weight, port and server, like 10 5 5060 sip.example.com.'
    case 'NS':
      return 'The name of a name server, like ns1.example.com. Not allowed on the domain itself.'
    default:
      return ''
  }
}

export function typePlaceholder(type: string): string {
  switch (type) {
    case 'A':
      return '203.0.113.10'
    case 'AAAA':
      return '2001:db8::10'
    case 'CNAME':
      return 'target.example.com'
    case 'TXT':
      return 'v=spf1 ~all'
    case 'MX':
      return '10 mail.example.com'
    case 'CAA':
      return '0 issue "letsencrypt.org"'
    case 'SRV':
      return '10 5 5060 sip.example.com'
    case 'NS':
      return 'ns1.example.com'
    default:
      return ''
  }
}

export function parseValues(text: string): string[] {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')
}

export function valuesToText(values: string[]): string {
  return values.join('\n')
}

const LABEL = /^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$/i
const NAME_LABEL = /^(\*|[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)$/i

export function isIPv4(value: string): boolean {
  const parts = value.split('.')
  return parts.length === 4 && parts.every((part) => /^(0|[1-9]\d{0,2})$/.test(part) && Number(part) <= 255)
}

export function isIPv6(value: string): boolean {
  if (!/^[0-9a-f:.]+$/i.test(value) || !value.includes(':')) return false
  let text = value
  const lastColon = text.lastIndexOf(':')
  const tail = text.slice(lastColon + 1)
  if (tail.includes('.')) {
    if (!isIPv4(tail)) return false
    text = `${text.slice(0, lastColon + 1)}0:0`
  }
  const halves = text.split('::')
  if (halves.length > 2) return false
  const groups = (part: string) => (part === '' ? [] : part.split(':'))
  const all = halves.flatMap(groups)
  const compressed = halves.length === 2
  if (compressed ? all.length > 7 : all.length !== 8) return false
  return all.every((group) => /^[0-9a-f]{1,4}$/i.test(group))
}

export function isHostname(value: string): boolean {
  const host = value.endsWith('.') ? value.slice(0, -1) : value
  if (host.length === 0 || host.length > 253) return false
  return host.split('.').every((label) => LABEL.test(label))
}

function isPort16(value: string): boolean {
  return /^\d{1,5}$/.test(value) && Number(value) <= 65535
}

function checkValue(type: string, value: string): string | null {
  switch (type) {
    case 'A':
      return isIPv4(value) ? null : `"${value}" isn't a valid IPv4 address. Use something like 203.0.113.10.`
    case 'AAAA':
      return isIPv6(value) ? null : `"${value}" isn't a valid IPv6 address. Use something like 2001:db8::10.`
    case 'CNAME':
    case 'NS':
      return isHostname(value) ? null : `"${value}" isn't a valid host name. Use something like target.example.com.`
    case 'TXT':
      if (value.length > 2000) return 'Each text line can have at most 2000 characters.'
      if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
        return 'Write the text without quotes.'
      }
      return null
    case 'MX': {
      const parts = value.split(/\s+/)
      if (parts.length !== 2 || !isPort16(parts[0])) {
        return `"${value}" should be a priority from 0 to 65535 and a mail server, like 10 mail.example.com.`
      }
      return parts[1] === '.' || isHostname(parts[1]) ? null : `"${parts[1]}" isn't a valid host name.`
    }
    case 'CAA': {
      const match = /^(\d{1,3})\s+([a-z0-9]+)\s+(\S.*)$/i.exec(value)
      if (!match || Number(match[1]) > 255) {
        return `"${value}" should be a flag, a tag and a value, like 0 issue "letsencrypt.org".`
      }
      return null
    }
    case 'SRV': {
      const parts = value.split(/\s+/)
      if (parts.length !== 4 || !parts.slice(0, 3).every(isPort16)) {
        return `"${value}" should be priority, weight, port and server, like 10 5 5060 sip.example.com.`
      }
      return parts[3] === '.' || isHostname(parts[3]) ? null : `"${parts[3]}" isn't a valid host name.`
    }
    default:
      return 'Choose a record type.'
  }
}

export type RecordDraft = {
  name: string
  type: string
  valuesText: string
  ttl: number | null
}

export type RecordDraftErrors = {
  name?: string
  type?: string
  values?: string
  ttl?: string
}

export function validateName(input: string, zone: string): string | null {
  const name = normalizeName(input)
  if (name === '') return 'Enter a name, or @ for the domain itself.'
  if (name === '@') return null
  if (name.endsWith('.')) return 'Leave out the dot at the end.'
  if (name === zone || name.endsWith(`.${zone}`)) {
    return `Enter the name without ".${zone}", like www. It's added for you.`
  }
  const labels = name.split('.')
  for (const [index, label] of labels.entries()) {
    if (label === '*' && index !== 0) return 'A * can only stand for the first part of the name.'
    if (!NAME_LABEL.test(label)) {
      return 'Use letters, numbers, hyphens and underscores. Each part can have up to 63 characters.'
    }
  }
  if (fullName(name, zone).length > 253) return 'This name is too long.'
  return null
}

export function validateTtl(ttl: number | null): string | null {
  if (ttl === null) return null
  if (!Number.isInteger(ttl) || ttl < TTL_MIN || ttl > TTL_MAX) {
    return `Use a whole number from ${TTL_MIN} to ${TTL_MAX} seconds, or leave it empty for the default.`
  }
  return null
}

export function validateValues(type: string, name: string, valuesText: string): string | null {
  const values = parseValues(valuesText)
  if (values.length === 0) return 'Enter at least one value.'
  if (type === 'CNAME') {
    if (normalizeName(name) === '@') return "A CNAME can't be on the domain itself."
    if (values.length > 1) return 'A CNAME can only have one value.'
  }
  if (type === 'NS' && normalizeName(name) === '@') return "NS records can't be on the domain itself."
  for (const value of values) {
    const problem = checkValue(type, value)
    if (problem) return problem
  }
  return null
}

export function validateRecordDraft(draft: RecordDraft, zone: string): RecordDraftErrors {
  const errors: RecordDraftErrors = {}
  const name = validateName(draft.name, zone)
  if (name) errors.name = name
  if (!(RECORD_TYPES as readonly string[]).includes(draft.type)) errors.type = 'Choose a record type.'
  else {
    const values = validateValues(draft.type, draft.name, draft.valuesText)
    if (values) errors.values = values
  }
  const ttl = validateTtl(draft.ttl)
  if (ttl) errors.ttl = ttl
  return errors
}

export function hasErrors(errors: RecordDraftErrors): boolean {
  return Boolean(errors.name || errors.type || errors.values || errors.ttl)
}

export function routeServerError(message: string): RecordDraftErrors & { form?: string } {
  const text = message.toLowerCase()
  if (/\bttl\b/.test(text)) return { ttl: message }
  if (/\bname\b/.test(text) && !/server|host/.test(text)) return { name: message }
  if (/address|value|priority|text|target|cname|host/.test(text)) return { values: message }
  return { form: message }
}
