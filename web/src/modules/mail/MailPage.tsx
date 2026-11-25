import type { ModuleProps } from '../types.ts'
import { MailDomainPage } from './MailDomainPage.tsx'
import { MailDomainsList } from './MailDomainsList.tsx'

export function MailPage({ segments }: ModuleProps) {
  const [domain] = segments
  return domain ? <MailDomainPage key={domain} domain={domain.toLowerCase()} /> : <MailDomainsList />
}
