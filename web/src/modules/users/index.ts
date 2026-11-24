import './users.css'
import { UsersPage } from './UsersPage.tsx'
import type { PanelModule } from '../types.ts'

export const usersModule: PanelModule = {
  id: 'users',
  label: 'People',
  basePath: '/people',
  component: UsersPage,
  visible: (user) => user.role === 'admin',
}
