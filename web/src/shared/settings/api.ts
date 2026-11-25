import { request } from '../api/index.ts'

export type DetectedAddress = { ip: string; public: boolean }

export type PanelSettings = {
  panel_domain: string
  panel_domain_locked: boolean
  server_ips: string[]
  server_ips_locked: boolean
  detected_ips: DetectedAddress[]
  acme_email: string
  acme_email_locked: boolean
  tls_mode: 'acme' | 'internal' | 'off'
  setup_complete: boolean
  ip_access: boolean
  mail_host: string
  mail_host_locked: boolean
  postmaster_address: string
  // Lists with a key come back hidden, like "••••.zen.dq.spamhaus.net"; sending them back unchanged keeps the key.
  mail_blocklists: string[]
  mail_blocklists_default: boolean
  mail_junk_score: number
  mail_greylisting: boolean
}

export type SettingsChange = {
  panel_domain?: string
  server_ips?: string[]
  acme_email?: string
  mail_host?: string
  postmaster_address?: string
  mail_blocklists?: string[]
  mail_blocklists_default?: boolean
  mail_junk_score?: number
  mail_greylisting?: boolean
}

export type DnsCheckState = 'points_here' | 'points_elsewhere' | 'no_record' | 'lookup_failed' | 'not_configured'
export type CertificateState = 'ready' | 'pending' | 'not_applicable' | 'not_configured'

export type PanelChecks = {
  dns: { state: DnsCheckState; found: string[]; expected: string[] }
  certificate: { state: CertificateState; detail: string }
}

function normalize(settings: PanelSettings): PanelSettings {
  return {
    ...settings,
    server_ips: settings.server_ips ?? [],
    detected_ips: settings.detected_ips ?? [],
    mail_host: settings.mail_host ?? '',
    mail_host_locked: settings.mail_host_locked ?? false,
    postmaster_address: settings.postmaster_address ?? '',
    mail_blocklists: settings.mail_blocklists ?? [],
    mail_blocklists_default: settings.mail_blocklists_default ?? true,
    mail_junk_score: settings.mail_junk_score ?? 5,
    mail_greylisting: settings.mail_greylisting ?? false,
  }
}

export async function getSettings(): Promise<PanelSettings> {
  return normalize(await request<PanelSettings>('/settings'))
}

export async function updateSettings(change: SettingsChange): Promise<PanelSettings> {
  return normalize(await request<PanelSettings>('/settings', { method: 'PUT', body: change }))
}

export async function getPanelChecks(signal?: AbortSignal): Promise<PanelChecks> {
  const checks = await request<PanelChecks>('/settings/checks', { signal })
  return {
    dns: { ...checks.dns, found: checks.dns.found ?? [], expected: checks.dns.expected ?? [] },
    certificate: checks.certificate,
  }
}

export async function finishSetup(): Promise<PanelSettings> {
  return normalize(await request<PanelSettings>('/settings/complete', { method: 'POST' }))
}
