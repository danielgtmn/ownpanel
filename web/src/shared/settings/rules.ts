import type { PanelChecks, PanelSettings } from './api.ts'

export const LOCKED_NOTE = 'Set in the server configuration'

export type Tone = 'positive' | 'warning' | 'neutral'

export type AddressOption = { ip: string; public: boolean }

export function isIPv6(ip: string): boolean {
  return ip.includes(':')
}

const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/
const IPV6 = /^[0-9a-f:.]+$/i

export function normalizeAddress(input: string): string {
  return input.trim().replace(/^\[(.*)\]$/, '$1').toLowerCase()
}

export function validateAddress(input: string, existing: string[] = []): string | null {
  const ip = normalizeAddress(input)
  if (!ip) return 'Enter an IP address.'
  const valid = isIPv6(ip) ? IPV6.test(ip) && ip.split('::').length <= 2 : IPV4.test(ip)
  if (!valid) return 'Enter an IP address like 203.0.113.5 or 2001:db8::1.'
  if (existing.includes(ip)) return 'This address is already in the list.'
  return null
}

// Detected addresses first, then saved and added ones the server didn't find on its own.
export function addressOptions(settings: PanelSettings, extra: string[]): AddressOption[] {
  const options: AddressOption[] = settings.detected_ips.map((detected) => ({ ip: detected.ip, public: detected.public }))
  for (const ip of [...settings.server_ips, ...extra]) {
    if (!options.some((option) => option.ip === ip)) options.push({ ip, public: true })
  }
  return options
}

export function initialAddressSelection(settings: PanelSettings): string[] {
  if (settings.server_ips.length > 0) return [...settings.server_ips]
  return settings.detected_ips.filter((detected) => detected.public).map((detected) => detected.ip)
}

export function sameAddresses(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const sorted = [...b].sort()
  return [...a].sort().every((ip, index) => ip === sorted[index])
}

export function normalizeDomain(input: string): string {
  return input
    .trim()
    .toLowerCase()
    .replace(/^[a-z]+:\/\//, '')
    .replace(/[/?#].*$/, '')
    .replace(/\.$/, '')
}

export function validateDomain(input: string): string | null {
  const domain = normalizeDomain(input)
  if (!domain) return 'Enter a domain, for example panel.example.com.'
  if (IPV4.test(domain) || isIPv6(domain)) return 'Enter a domain name like panel.example.com, not an IP address.'
  if (!/^[a-z0-9-]+(\.[a-z0-9-]+)+$/.test(domain) || domain.split('.').some((label) => !label || label.startsWith('-') || label.endsWith('-'))) {
    return 'Enter a domain like panel.example.com, without spaces or other characters.'
  }
  return null
}

export function validateEmail(input: string): string | null {
  const email = input.trim()
  if (!email) return null
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return 'Enter an e-mail address like you@example.com.'
  return null
}

export type RecordRow = { type: 'A' | 'AAAA'; name: string; value: string }

export function recordRows(domain: string, ips: string[]): RecordRow[] {
  if (!domain) return []
  const rows = ips.map((ip): RecordRow => ({ type: isIPv6(ip) ? 'AAAA' : 'A', name: domain, value: ip }))
  return [...rows.filter((row) => row.type === 'A'), ...rows.filter((row) => row.type === 'AAAA')]
}

export function dnsText(dns: PanelChecks['dns']): string {
  switch (dns.state) {
    case 'points_here':
      return 'Points to this server'
    case 'points_elsewhere':
      return dns.found.length > 0 ? `Not pointing here yet (found ${dns.found.join(', ')})` : 'Not pointing here yet'
    case 'no_record':
      return 'No record yet'
    case 'lookup_failed':
      return "Couldn't be checked right now"
    case 'not_configured':
      return 'No panel domain set'
  }
}

export function dnsTone(dns: PanelChecks['dns']): Tone {
  if (dns.state === 'points_here') return 'positive'
  if (dns.state === 'points_elsewhere' || dns.state === 'no_record') return 'warning'
  return 'neutral'
}

export function certificateText(certificate: PanelChecks['certificate']): string {
  switch (certificate.state) {
    case 'ready':
      return 'Ready'
    case 'pending':
      return 'Waiting for the certificate'
    case 'not_applicable':
      return 'Not needed'
    case 'not_configured':
      return 'No panel domain set'
  }
}

export function certificateTone(certificate: PanelChecks['certificate']): Tone {
  if (certificate.state === 'ready') return 'positive'
  if (certificate.state === 'pending') return 'warning'
  return 'neutral'
}

export function certificateDone(checks: PanelChecks): boolean {
  return checks.certificate.state === 'ready' || checks.certificate.state === 'not_applicable'
}

export function panelReady(checks: PanelChecks): boolean {
  return checks.dns.state === 'points_here' && certificateDone(checks)
}

// Nothing left to wait for: everything is ready, or there is no domain to check.
export function checksSettled(checks: PanelChecks): boolean {
  return panelReady(checks) || checks.dns.state === 'not_configured'
}

export function panelUrl(domain: string, port = ''): string {
  return port && port !== '443' ? `https://${domain}:${port}` : `https://${domain}`
}

export function domainChangeQuestion(current: string, next: string) {
  if (!next) {
    return {
      title: `Remove ${current} as the panel's address?`,
      description: 'The panel will be reachable by IP address again, with a browser warning.',
      confirmLabel: 'Remove domain',
    }
  }
  return {
    title: `Move the panel to ${next}?`,
    description: `${current} stops working once ${next} is ready. Make sure the DNS records for ${next} are in place.`,
    confirmLabel: 'Change domain',
  }
}
