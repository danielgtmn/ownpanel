import { vi } from 'vitest'
import type { PanelChecks, PanelSettings } from './api.ts'

export function settingsFixture(overrides: Partial<PanelSettings> = {}): PanelSettings {
  return {
    panel_domain: '',
    panel_domain_locked: false,
    server_ips: [],
    server_ips_locked: false,
    detected_ips: [
      { ip: '203.0.113.5', public: true },
      { ip: '2001:db8::5', public: true },
      { ip: '192.168.1.20', public: false },
    ],
    acme_email: '',
    acme_email_locked: false,
    tls_mode: 'acme',
    setup_complete: false,
    ip_access: true,
    mail_host: '',
    mail_host_locked: false,
    postmaster_address: '',
    preview_domain: '',
    mail_blocklists: ['zen.spamhaus.org'],
    mail_blocklists_default: true,
    mail_junk_score: 5,
    mail_greylisting: false,
    ...overrides,
  }
}

export function checksFixture(
  dns: PanelChecks['dns']['state'],
  certificate: PanelChecks['certificate']['state'],
  found: string[] = [],
): PanelChecks {
  return {
    dns: { state: dns, found, expected: ['203.0.113.5'] },
    certificate: { state: certificate, detail: '' },
  }
}

export const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

export function mockFetch(...responses: Array<Response | Error>) {
  const queue = [...responses]
  const fn = vi.fn(async (_url: string, _init?: RequestInit) => {
    const next = queue.shift() ?? json({})
    if (next instanceof Error) throw next
    return next
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

export const call = (fn: ReturnType<typeof mockFetch>, index: number) => fn.mock.calls[index] as [string, RequestInit]
