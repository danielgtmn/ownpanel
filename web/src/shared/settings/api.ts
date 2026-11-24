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
}

export type SettingsChange = {
  panel_domain?: string
  server_ips?: string[]
  acme_email?: string
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
