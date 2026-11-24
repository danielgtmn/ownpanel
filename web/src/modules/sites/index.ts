import './sites.css'
import { SitesPage } from './SitesPage.tsx'
import type { PanelModule } from '../types.ts'

export const sitesModule: PanelModule = {
  id: 'sites',
  label: 'Sites',
  basePath: '/sites',
  component: SitesPage,
}
