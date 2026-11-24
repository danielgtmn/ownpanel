import './access.css'
import { AccessPage } from './AccessPage.tsx'
import { KeyIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export const accessModule: PanelModule = {
  id: 'access',
  label: 'Access tokens',
  basePath: '/tokens',
  railEntry: { icon: KeyIcon },
  component: AccessPage,
}
