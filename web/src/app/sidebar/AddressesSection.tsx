import { Button } from '@ui-sdk/react/button'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input, InputGroup } from '@ui-sdk/react/input'
import { toast } from '@ui-sdk/react/toast'
import { useMemo, useState, type FormEvent } from 'react'
import {
  createRedirect,
  errorMessage,
  isSavedButNotApplied,
  saveSiteDomains,
  type RedirectInput,
} from '../../shared/api/index.ts'
import { DomainCheck } from '../../shared/domains/index.ts'
import {
  addressActions,
  checksOnLoad,
  collectAddresses,
  subdomainHost,
  type Address,
  type AddressAction,
  type DomainSources,
  type Family,
  type SourceSite,
} from '../../shared/families/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { RedirectDialog } from '../../shared/redirects/index.ts'
import { hrefFor, hrefWithQuery, newSiteHref } from '../../shared/routing/index.ts'
import { Section, StatusBadge } from '../../shared/ui/index.ts'
import { AddToSiteDialog } from './AddToSiteDialog.tsx'
import { loadZoneRecords } from './data.ts'

const ACTION_TEXT: Record<AddressAction, string> = {
  'add-website': 'Add website',
  'add-redirect': 'Add redirect',
  'add-to-website': 'Add to a website',
}

function AddressUse({ address, domain }: { address: Address; domain: string }) {
  if (address.wildcard) return <p className="muted address-use">Answers for every subdomain without its own entry</p>
  const use = address.use
  if (use.kind === 'site') {
    return (
      <p className="address-use">
        <span>
          Website <a href={hrefFor('sites', use.site)}>{use.site}</a>
        </span>
        <StatusBadge tone={use.live ? 'positive' : 'warning'}>{use.live ? 'Live' : 'No content yet'}</StatusBadge>
      </p>
    )
  }
  if (use.kind === 'redirect') {
    return (
      <p className="address-use">
        <span>
          Redirects to <a href={hrefWithQuery(['redirects'], { family: domain })}>{use.target}</a>
        </span>
      </p>
    )
  }
  return <p className="muted address-use">Not used</p>
}

type RowProps = {
  address: Address
  domain: string
  actions: AddressAction[]
  checked: boolean
  onAction: (action: AddressAction, host: string) => void
}

function AddressRow({ address, domain, actions, checked, onAction }: RowProps) {
  const { host } = address
  return (
    <li className="record-row address-row">
      <div className="address-main">
        <h3 className="address-host">{host}</h3>
        <AddressUse address={address} domain={domain} />
        {checked ? <DomainCheck domain={host} /> : null}
      </div>
      {actions.length > 0 ? (
        <div className="record-actions">
          {actions.map((action) => (
            <Button key={action} onClick={() => onAction(action, host)}>
              {ACTION_TEXT[action]}
              <span className="visually-hidden"> for {host}</span>
            </Button>
          ))}
        </div>
      ) : null}
    </li>
  )
}

function AddSubdomain({
  domain,
  addresses,
  actions,
  onAction,
}: {
  domain: string
  addresses: Address[]
  actions: AddressAction[]
  onAction: (action: AddressAction, host: string) => void
}) {
  const [label, setLabel] = useState('')
  const [error, setError] = useState<string | null>(null)

  function run(action: AddressAction | null) {
    const result = subdomainHost(label, domain)
    if ('error' in result) {
      setError(result.error)
      return
    }
    const existing = addresses.find((address) => address.host === result.host)
    if (existing && existing.use.kind !== 'none') {
      setError(`${result.host} is already in use, see its row above.`)
      return
    }
    if (!action) return
    setError(null)
    setLabel('')
    onAction(action, result.host)
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    run(null)
  }

  return (
    <form className="form address-add" onSubmit={submit} noValidate aria-label="Add subdomain">
      <Field name="subdomain" invalid={error ? true : undefined}>
        <FieldLabel>Add subdomain</FieldLabel>
        <InputGroup className="address-input">
          <Input
            value={label}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="shop"
            onValueChange={(value) => {
              setLabel(value)
              setError(null)
            }}
          />
          <span className="address-suffix">.{domain}</span>
        </InputGroup>
        <FieldDescription>Letters, digits and hyphens. Then choose what it shows.</FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      <div className="record-actions">
        {actions.map((action) => (
          <Button key={action} type="button" onClick={() => run(action)}>
            {ACTION_TEXT[action]}
            <span className="visually-hidden"> for the new subdomain</span>
          </Button>
        ))}
      </div>
    </form>
  )
}

export function AddressesSection({ family, sources }: { family: Family; sources: DomainSources }) {
  const zoneKey = family.zones.map((zone) => `${zone.connectionId}/${zone.name}`).join(',')
  const records = useResource(() => loadZoneRecords(family.zones), `zone-records:${zoneKey}`)
  const zones = records.status === 'ready' ? records.data.zones : undefined
  const addresses = useMemo(() => collectAddresses(family.domain, sources, zones), [family.domain, sources, zones])
  const [redirectHost, setRedirectHost] = useState<string | null>(null)
  const [siteHost, setSiteHost] = useState<string | null>(null)
  const siteCount = sources.sites.length
  const newActions = addressActions({ host: '', wildcard: false, use: { kind: 'none' } }, siteCount)

  function act(action: AddressAction, host: string) {
    if (action === 'add-website') window.location.hash = newSiteHref(host)
    else if (action === 'add-redirect') setRedirectHost(host)
    else setSiteHost(host)
  }

  async function saveRedirect(input: RedirectInput) {
    try {
      await createRedirect(input)
      toast.success(`${input.source_domain} now redirects to ${input.target}.`)
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setRedirectHost(null)
  }

  async function addToSite(site: SourceSite, host: string) {
    try {
      await saveSiteDomains(site.name, [...site.domains, host])
      toast.success(`${host} now shows the website ${site.name}.`)
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setSiteHost(null)
  }

  return (
    <Section title="Addresses" description={`Every address under ${family.domain} and what visitors see there.`}>
      <ul className="record-list" aria-label={`Addresses of ${family.domain}`}>
        {addresses.map((address) => (
          <AddressRow
            key={address.host}
            address={address}
            domain={family.domain}
            actions={addressActions(address, siteCount)}
            checked={checksOnLoad(address)}
            onAction={act}
          />
        ))}
      </ul>
      {records.status === 'loading' && family.zones.length > 0 ? (
        <p className="muted">Looking for more addresses in your DNS records…</p>
      ) : null}
      {records.status === 'ready' && records.data.failed.length > 0 ? (
        <p className="muted">
          Couldn't read the DNS records of {records.data.failed.join(', ')}, so addresses that exist only there are
          missing. <Button onClick={records.retry}>Try again</Button>
        </p>
      ) : null}
      <AddSubdomain domain={family.domain} addresses={addresses} actions={newActions} onAction={act} />
      <RedirectDialog
        open={redirectHost !== null}
        onOpenChange={(open) => {
          if (!open) setRedirectHost(null)
        }}
        kind="domain"
        existing={null}
        domains={[]}
        initialDomain={redirectHost ?? undefined}
        onSubmit={saveRedirect}
      />
      <AddToSiteDialog
        host={siteHost}
        sites={[...sources.sites].sort((a, b) => a.name.localeCompare(b.name))}
        onOpenChange={(open) => {
          if (!open) setSiteHost(null)
        }}
        onSubmit={addToSite}
      />
    </Section>
  )
}
