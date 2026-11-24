import './redirects.css'
import { RedirectsPage } from './RedirectsPage.tsx'
import type { PanelModule } from '../types.ts'

export const redirectsModule: PanelModule = {
  id: 'redirects',
  label: 'Redirects',
  basePath: '/redirects',
  component: RedirectsPage,
}
