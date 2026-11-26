import { request, type ChangedRecord } from '../../shared/api/index.ts'
import { normalizeDomain, validateDomain, type PanelSettings } from '../../shared/settings/index.ts'

/** Writes the record for all preview addresses into the zone of a connected account. */
export async function pointPreviewDomain(): Promise<ChangedRecord[]> {
  const result = await request<{ changed: ChangedRecord[] | null }>('/settings/preview-domain/point', { method: 'POST' })
  return result.changed ?? []
}

/** The domain suggested for previews: one below the panel's address, if the panel has one. */
export function suggestedPreviewDomain(settings: Pick<PanelSettings, 'panel_domain'>): string {
  return settings.panel_domain ? `preview.${settings.panel_domain}` : ''
}

/** A problem with the entered preview domain, or null. Empty turns preview addresses off. */
export function validatePreviewDomain(input: string, settings: Pick<PanelSettings, 'panel_domain'>): string | null {
  const domain = normalizeDomain(input)
  if (!domain) return null
  if (domain.startsWith('*.')) return 'Enter the domain without "*.", for example preview.example.com.'
  const problem = validateDomain(domain)
  if (problem) return problem.replace('panel.example.com', 'preview.example.com')
  if (settings.panel_domain && (domain === settings.panel_domain || settings.panel_domain.endsWith(`.${domain}`))) {
    return "The panel's own address can't be part of it. Use a domain of its own, like " + suggestedPreviewDomain(settings) + '.'
  }
  return null
}
