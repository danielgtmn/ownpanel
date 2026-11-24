// The daemon uses the full public suffix list, but no endpoint exposes it, and the npm copies
// (psl, tldts) are several hundred kilobytes. This heuristic covers the usual two-label suffixes
// (co.uk, com.au, co.jp, …). Private suffixes such as github.io and rare ccTLD rules aren't known,
// so blog.user.github.io groups under github.io, and a name like com.de counts as a suffix.
const SECOND_LEVEL = new Set([
  'ac',
  'co',
  'com',
  'edu',
  'gob',
  'go',
  'gov',
  'id',
  'ltd',
  'me',
  'mil',
  'ne',
  'net',
  'nic',
  'nom',
  'or',
  'org',
  'plc',
  'sch',
])

const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/

export function normalizeHost(value: string): string {
  return value.trim().toLowerCase().replace(/\.$/, '')
}

export function registrableDomain(value: string): string {
  const host = normalizeHost(value)
  if (!host || IPV4.test(host) || host.includes(':')) return host
  const labels = host.split('.')
  if (labels.length <= 2) return host
  const tld = labels[labels.length - 1]
  const second = labels[labels.length - 2]
  const suffixLength = tld.length === 2 && SECOND_LEVEL.has(second) ? 2 : 1
  return labels.slice(-(suffixLength + 1)).join('.')
}
