import './settings.css'
import { SettingsPage } from './SettingsPage.tsx'
import { SettingsIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export const settingsModule: PanelModule = {
  id: 'settings',
  label: 'Settings',
  basePath: '/settings',
  railEntry: { icon: SettingsIcon },
  component: SettingsPage,
  visible: (user) => user.role === 'admin',
}
