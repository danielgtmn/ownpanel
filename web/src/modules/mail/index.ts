import './mail.css'
import { startsWith, type Family, type FamilyRow } from '../../shared/families/index.ts'
import { hrefFor } from '../../shared/routing/index.ts'
import { MailIcon } from '../../shared/ui/index.ts'
import { MailPage } from './MailPage.tsx'
import { sidebarStatus } from './rules.ts'
import type { PanelModule } from '../types.ts'

export function mailRows(family: Family): FamilyRow[] {
  return family.mailDomains.map((mail) => {
    const { status, text } = sidebarStatus(mail.active)
    return {
      key: `mail:${mail.domain}`,
      label: 'E-mail',
      hint: family.mailDomains.length > 1 || mail.domain !== family.domain ? mail.domain : undefined,
      href: hrefFor('mail', mail.domain),
      icon: MailIcon,
      status,
      statusText: text,
      isCurrent: (route) => startsWith(route.segments, ['mail', mail.domain]),
    }
  })
}

export const mailModule: PanelModule = {
  id: 'mail',
  label: 'E-mail',
  basePath: '/mail',
  component: MailPage,
  overviewRow: { icon: MailIcon, count: (sources) => sources.mailDomains?.length ?? 0 },
  familyRows: mailRows,
}
