import './activity.css'
import { ActivityPage } from './ActivityPage.tsx'
import type { PanelModule } from '../types.ts'

export const activityModule: PanelModule = {
  id: 'activity',
  label: 'Activity',
  basePath: '/activity',
  component: ActivityPage,
  visible: (user) => user.role === 'admin',
}
