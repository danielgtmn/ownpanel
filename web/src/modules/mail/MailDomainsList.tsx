import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage, isSavedButNotApplied } from '../../shared/api/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { clearQuery, hrefFor, navigate, prefilledDomain, useQuery } from '../../shared/routing/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { EmptyState, ErrorState, LoadingState, PageHeader, Section, StatusBadge } from '../../shared/ui/index.ts'
import { enableMailDomain, listMailDomains } from './api.ts'
import { EnableMailDialog } from './EnableMailDialog.tsx'
import { domainState } from './rules.ts'

const plural = (count: number, one: string, many: string) => `${count} ${count === 1 ? one : many}`

export function MailDomainsList() {
  const domains = useResource(listMailDomains, 'mail-domains')
  const { isAdmin } = useSession()
  const [open, setOpen] = useState(false)
  const query = useQuery()
  const wantsNew = query.has('new')
  const prefilled = wantsNew ? prefilledDomain(query) : undefined
  usePageTitle('E-mail')

  async function enable(domain: string) {
    try {
      await enableMailDomain(domain)
      toast.success(`E-mail is on for ${domain}.`)
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setOpen(false)
    clearQuery()
    navigate('mail', domain)
  }

  const addButton = (
    <Button data-variant="solid" onClick={() => setOpen(true)}>
      Turn on e-mail
    </Button>
  )
  const list = domains.status === 'ready' ? domains.data : []

  return (
    <>
      <PageHeader
        title="E-mail"
        description="Mailboxes and forwarding addresses on your domains."
        actions={list.length > 0 ? addButton : null}
      />
      <Section title="Domains with e-mail">
        {domains.status === 'loading' ? <LoadingState label="Loading e-mail…" /> : null}
        {domains.status === 'error' ? (
          <ErrorState title="Couldn't load e-mail" message={errorMessage(domains.error)} onRetry={domains.retry} />
        ) : null}
        {domains.status === 'ready' && list.length === 0 ? (
          <EmptyState
            inSection
            title="No e-mail yet"
            description="Turn on e-mail for one of your domains to create mailboxes like info@ or you@ on it."
            action={addButton}
          />
        ) : null}
        {list.length > 0 ? (
          <ul className="record-list" aria-label="Domains with e-mail">
            {list.map((mail) => {
              const state = domainState(mail)
              return (
                <li className="record-row" key={mail.domain}>
                  <div className="record-main">
                    <h2 className="record-title">
                      <a href={hrefFor('mail', mail.domain)}>{mail.domain}</a>
                    </h2>
                    <ul className="record-facts">
                      <li>{plural(mail.mailboxes, 'mailbox', 'mailboxes')}</li>
                      <li>{plural(mail.aliases, 'forwarding address', 'forwarding addresses')}</li>
                      {isAdmin && mail.owner ? <li>Owner: {mail.owner}</li> : null}
                    </ul>
                  </div>
                  <StatusBadge tone={state.tone}>{state.label}</StatusBadge>
                </li>
              )
            })}
          </ul>
        ) : null}
      </Section>
      <EnableMailDialog
        open={open || wantsNew}
        onOpenChange={(next) => {
          setOpen(next)
          if (!next) clearQuery()
        }}
        initialDomain={prefilled}
        onSubmit={enable}
      />
    </>
  )
}
