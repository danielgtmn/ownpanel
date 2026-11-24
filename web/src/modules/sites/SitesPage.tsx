import type { ModuleProps } from '../types.ts'
import { SiteDetail } from './SiteDetail.tsx'
import { SitesList } from './SitesList.tsx'

export function SitesPage({ segments }: ModuleProps) {
  const [name] = segments
  return name ? <SiteDetail key={name} name={name} /> : <SitesList />
}
