// What a person may do with a mailbox someone shares with them, as the panel
// and the account area both show it (internal/mail/config: grants; wiki
// Groupware-Architecture.md, G3). The values are the API's; people only see
// the labels.

export const RIGHTS = ['read', 'write', 'manage', 'send-as', 'send-on-behalf'] as const
export type Right = (typeof RIGHTS)[number]

export const RIGHT_LABEL: Record<Right, string> = {
  read: 'Read',
  write: 'Write',
  manage: 'Manage filters and out-of-office',
  'send-as': 'Send as',
  'send-on-behalf': 'Send on behalf',
}

export const RIGHT_HINT: Record<Right, string> = {
  read: 'See its folders and messages. Each person keeps their own read and unread marks.',
  write: 'Change, move and delete messages, and create folders. Includes Read.',
  manage: 'Change its filters and its out-of-office reply.',
  'send-as': 'Send mail that shows only this address as the sender.',
  'send-on-behalf': 'Send mail from this address that also names the person who sent it.',
}

function isRight(value: string): value is Right {
  return (RIGHTS as readonly string[]).includes(value)
}

// Known rights only, each once, in the fixed order; Write always brings Read.
export function normaliseRights(rights: readonly string[]): Right[] {
  const set = new Set(rights.filter(isRight))
  if (set.has('write')) set.add('read')
  return RIGHTS.filter((right) => set.has(right))
}

// Ticking or unticking one right. Unticking Read also takes Write away,
// because Write can't go without it (the form keeps Read ticked while Write is).
export function toggleRight(rights: readonly string[], right: Right, on: boolean): Right[] {
  const current = normaliseRights(rights)
  if (on) return normaliseRights([...current, right])
  return current.filter((existing) => existing !== right && !(right === 'read' && existing === 'write'))
}

// "Read, Write, Send as"; Write already says Read, so Read is left out then.
export function rightsText(rights: readonly string[]): string {
  const list = normaliseRights(rights)
  const shown = list.includes('write') ? list.filter((right) => right !== 'read') : list
  if (shown.length === 0) return 'No rights'
  return shown.map((right) => (right === 'write' ? 'Read and write' : RIGHT_LABEL[right])).join(', ')
}

export function validateRights(rights: readonly string[]): string | null {
  return normaliseRights(rights).length === 0 ? 'Choose at least one thing the person may do.' : null
}

const ADDRESS = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export function validatePersonAddress(value: string): string | null {
  const address = value.trim()
  if (!address) return 'Enter the address of the person’s mailbox.'
  if (!ADDRESS.test(address)) return 'Enter a whole e-mail address, like anna@example.com.'
  return null
}

export type Grant = { address: string; rights: string[] }

// The list with one person added or their rights changed; addresses compare without case.
export function withGrant<T extends Grant>(list: readonly T[], address: string, rights: readonly string[]): Grant[] {
  const key = address.trim().toLowerCase()
  const next: Grant = { address: key, rights: normaliseRights(rights) }
  let found = false
  const result: Grant[] = list.map((grant) => {
    if (grant.address.toLowerCase() !== key) return { address: grant.address, rights: normaliseRights(grant.rights) }
    found = true
    return next
  })
  if (!found) result.push(next)
  return result
}

export function withoutGrant<T extends Grant>(list: readonly T[], address: string): Grant[] {
  const key = address.trim().toLowerCase()
  return list
    .filter((grant) => grant.address.toLowerCase() !== key)
    .map((grant) => ({ address: grant.address, rights: normaliseRights(grant.rights) }))
}

// "anna@example.com", "anna@example.com and ben@example.com", "anna@example.com, ben@example.com and 3 more".
export function addressList(addresses: readonly string[], shown = 2): string {
  if (addresses.length <= shown) {
    if (addresses.length <= 1) return addresses.join('')
    return `${addresses.slice(0, -1).join(', ')} and ${addresses[addresses.length - 1]}`
  }
  return `${addresses.slice(0, shown).join(', ')} and ${addresses.length - shown} more`
}
