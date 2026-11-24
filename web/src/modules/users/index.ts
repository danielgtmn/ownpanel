import './users.css'
import { UsersPage } from './UsersPage.tsx'
import { PeopleIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export const usersModule: PanelModule = {
  id: 'users',
  label: 'People',
  basePath: '/people',
  railEntry: { icon: PeopleIcon },
  component: UsersPage,
  visible: (user) => user.role === 'admin',
}
