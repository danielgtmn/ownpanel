import './settings.css'
import { SettingsPage } from './SettingsPage.tsx'
import type { PanelModule } from '../types.ts'

export const settingsModule: PanelModule = {
  id: 'settings',
  label: 'Settings',
  basePath: '/settings',
  component: SettingsPage,
  visible: (user) => user.role === 'admin',
}
