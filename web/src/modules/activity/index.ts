import './activity.css'
import { ActivityPage } from './ActivityPage.tsx'
import { ActivityIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export const activityModule: PanelModule = {
  id: 'activity',
  label: 'Activity',
  basePath: '/activity',
  railEntry: { icon: ActivityIcon },
  component: ActivityPage,
  visible: (user) => user.role === 'admin',
}
