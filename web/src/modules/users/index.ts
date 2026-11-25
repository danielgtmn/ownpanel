import './users.css'
import { PeopleIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export const usersModule: PanelModule = {
  id: 'users',
  label: 'People',
  basePath: '/people',
  railEntry: { icon: PeopleIcon },
  page: () => import('./UsersPage.tsx').then((page) => page.UsersPage),
  visible: (user) => user.role === 'admin',
}
