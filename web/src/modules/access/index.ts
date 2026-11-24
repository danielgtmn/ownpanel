import './access.css'
import { AccessPage } from './AccessPage.tsx'
import type { PanelModule } from '../types.ts'

export const accessModule: PanelModule = {
  id: 'access',
  label: 'Access tokens',
  basePath: '/tokens',
  component: AccessPage,
}
