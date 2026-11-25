import './access.css'
import { KeyIcon } from '../../shared/ui/index.ts'
import type { PanelModule } from '../types.ts'

export const accessModule: PanelModule = {
  id: 'access',
  label: 'Access tokens',
  basePath: '/tokens',
  railEntry: { icon: KeyIcon },
  page: () => import('./AccessPage.tsx').then((page) => page.AccessPage),
}
