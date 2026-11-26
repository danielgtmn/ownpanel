import './settings.css'
import { SettingsIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export const settingsModule: PanelModule = {
  id: 'settings',
  label: 'Settings',
  basePath: '/settings',
  railEntry: { icon: SettingsIcon },
  page: () => import('./SettingsPage.tsx').then((page) => page.SettingsPage),
}
