import './dns.css'
import { DnsPage } from './DnsPage.tsx'
import type { PanelModule } from '../types.ts'

export const dnsModule: PanelModule = {
  id: 'dns',
  label: 'DNS',
  basePath: '/dns',
  component: DnsPage,
}
