import type { PanelSettings } from '../../shared/settings/index.ts'

export function ipAccessNotice(settings: Pick<PanelSettings, 'ip_access' | 'setup_complete'>): string | null {
  if (!settings.ip_access || !settings.setup_complete) return null
  return 'The panel is reachable by IP address because no panel domain is set. Browsers show a warning there; set a panel domain below to get a trusted certificate.'
}
