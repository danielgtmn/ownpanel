import './activity.css'
import { ActivityIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export const activityModule: PanelModule = {
  id: 'activity',
  label: 'Activity',
  basePath: '/activity',
  railEntry: { icon: ActivityIcon },
  page: () => import('./ActivityPage.tsx').then((page) => page.ActivityPage),
  visible: (user) => user.role === 'admin',
}
