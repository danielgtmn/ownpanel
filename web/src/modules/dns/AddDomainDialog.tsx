import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectGroupLabel,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@ui-sdk/react/select'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { groupByAccount, zoneKey, zonesNamed, type Connection, type Zone, type ZoneList } from '../../shared/dns/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { connectAccountHref } from '../../shared/routing/index.ts'
import { ErrorState, FormDialog, LoadingState } from '../../shared/ui/index.ts'
import { listAvailableZones } from './api.ts'
import { Nameservers } from './Nameservers.tsx'
import { normalizeZoneName, problemMessage, validateZoneName } from './zones.ts'

type Mode = 'choose' | 'create'

type FormProps = {
  accounts: Connection[]
  available: ZoneList
  domain?: string
  onAdopt: (zone: Zone) => Promise<void>
  onCreate: (connection: Connection, name: string) => Promise<void>
}

function AccountSelect({ accounts, value, onChange }: { accounts: Connection[]; value: string; onChange: (id: string) => void }) {
  return (
    <Field name="account">
      <FieldLabel>Account</FieldLabel>
      <Select value={value} onValueChange={(next) => next && onChange(next)}>
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {accounts.map((account) => (
            <SelectItem key={account.id} value={account.id}>
              {account.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}

export function AddDomainForm({ accounts, available, domain, onAdopt, onCreate }: FormProps) {
  const groups = groupByAccount(available.zones)
  const preselected = domain ? zonesNamed(available.zones, domain)[0] : undefined
  const [mode, setMode] = useState<Mode>(available.zones.length > 0 && (!domain || preselected) ? 'choose' : 'create')
  const first = groups[0]?.zones[0]
  const [picked, setPicked] = useState(preselected ? zoneKey(preselected) : first ? zoneKey(first) : '')
  const [account, setAccount] = useState(accounts[0]?.id ?? '')
  const [name, setName] = useState(preselected ? '' : (domain ?? ''))
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    let run: () => Promise<void>
    if (mode === 'choose') {
      const zone = available.zones.find((candidate) => zoneKey(candidate) === picked)
      if (!zone) {
        setError('Choose a domain.')
        return
      }
      run = () => onAdopt(zone)
    } else {
      const connection = accounts.find((candidate) => candidate.id === account)
      const problem = validateZoneName(name) ?? (connection ? null : 'Choose an account.')
      if (problem || !connection) {
        setError(problem)
        return
      }
      run = () => onCreate(connection, normalizeZoneName(name))
    }
    setBusy(true)
    try {
      await run()
    } catch (caught) {
      setError(errorMessage(caught))
      setBusy(false)
    }
  }

  const choose = (
    <Field name="domain" invalid={error ? true : undefined}>
      <FieldLabel>Domain</FieldLabel>
      <Select
        value={picked}
        onValueChange={(value) => {
          if (value) setPicked(value)
          setError(null)
        }}
      >
        <SelectTrigger>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {groups.length > 1
            ? groups.map((group) => (
                <SelectGroup key={group.connectionId}>
                  <SelectGroupLabel>{group.connectionName}</SelectGroupLabel>
                  {group.zones.map((zone) => (
                    <SelectItem key={zoneKey(zone)} value={zoneKey(zone)}>
                      {zone.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              ))
            : available.zones.map((zone) => (
                <SelectItem key={zoneKey(zone)} value={zoneKey(zone)}>
                  {zone.name}
                </SelectItem>
              ))}
        </SelectContent>
      </Select>
      <FieldDescription>
        {groups.length === 1
          ? `Domains in ${groups[0].connectionName} that aren't managed here yet. Nothing changes at the provider.`
          : "Domains in your accounts that aren't managed here yet. Nothing changes at the provider."}
      </FieldDescription>
      {error ? <FieldError match>{error}</FieldError> : null}
    </Field>
  )

  const create = (
    <>
      {accounts.length > 1 ? <AccountSelect accounts={accounts} value={account} onChange={setAccount} /> : null}
      <Field name="name" invalid={error ? true : undefined}>
        <FieldLabel>New domain</FieldLabel>
        <Input
          value={name}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder="example.com"
          onValueChange={(value) => {
            setName(value)
            setError(null)
          }}
        />
        <FieldDescription>
          The domain you registered, like example.com, without www. It's created
          {accounts.length === 1 ? ` in ${accounts[0].name}` : ' in the account you choose'}.
        </FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
    </>
  )

  return (
    <form className="form" onSubmit={submit} noValidate>
      {available.problems.map((problem) => (
        <p className="notice notice-problem" role="status" key={problem.connection_id}>
          {problemMessage(problem)}
        </p>
      ))}
      <Field>
        <FieldLabel className="visually-hidden">How do you want to add the domain?</FieldLabel>
        <RadioGroup
          value={mode}
          onValueChange={(value) => {
            if (value) setMode(value as Mode)
            setError(null)
          }}
        >
          <Field>
            <Radio value="choose" disabled={available.zones.length === 0}>
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Choose one from your accounts</FieldLabel>
              <FieldDescription>
                {available.zones.length === 0
                  ? 'Every domain in your accounts is managed here already.'
                  : 'Manage the DNS records of a domain your account already has.'}
              </FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="create">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Create a new one</FieldLabel>
              <FieldDescription>For a domain your account doesn't have yet.</FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>
      {mode === 'choose' ? choose : create}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Adding…' : 'Add domain'}
        </Button>
      </div>
    </form>
  )
}

function Created({ zone }: { zone: Zone }) {
  return (
    <div className="form">
      <p className="dialog-text">
        Enter these nameservers for {zone.name} where you registered it. The domain works once the registrar uses them,
        which can take up to a day.
      </p>
      <Nameservers nameservers={zone.nameservers} domain={zone.name} />
      <div className="form-actions">
        <DialogClose data-variant="solid">Done</DialogClose>
      </div>
    </div>
  )
}

function NoAccount() {
  return (
    <div className="form">
      <p className="dialog-text">
        To manage the DNS records of a domain here, first connect the account at the company that manages it.
      </p>
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button data-variant="solid" onClick={() => (window.location.hash = connectAccountHref())}>
          Connect an account
        </Button>
      </div>
    </div>
  )
}

function Body({ accounts, domain, onAdopt, onCreate }: Omit<FormProps, 'available'>) {
  const available = useResource(listAvailableZones, 'dns-available-zones')
  if (available.status === 'loading') return <LoadingState label="Loading the domains of your accounts…" />
  if (available.status === 'error') {
    return (
      <ErrorState title="Couldn't load your domains" message={errorMessage(available.error)} onRetry={available.retry} />
    )
  }
  return <AddDomainForm accounts={accounts} available={available.data} domain={domain} onAdopt={onAdopt} onCreate={onCreate} />
}

type Props = {
  open: boolean
  accounts: Connection[] | null
  domain?: string
  onOpenChange: (open: boolean) => void
  onAdopt: (zone: Zone) => Promise<void>
  onCreate: (connection: Connection, name: string) => Promise<Zone>
}

export function AddDomainDialog({ open, accounts, domain, onOpenChange, onAdopt, onCreate }: Props) {
  const [created, setCreated] = useState<Zone | null>(null)

  return (
    <FormDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setCreated(null)
        onOpenChange(next)
      }}
      title={created ? `${created.name} was added` : 'Add domain'}
      description={
        created
          ? `${created.name} is now at ${created.connection_name} and managed here.`
          : 'Manage the DNS records of a domain here.'
      }
    >
      {created ? (
        <Created zone={created} />
      ) : accounts === null ? (
        <LoadingState label="Loading your accounts…" />
      ) : accounts.length === 0 ? (
        <NoAccount />
      ) : (
        <Body
          accounts={accounts}
          domain={domain}
          onAdopt={onAdopt}
          onCreate={async (connection, name) => setCreated(await onCreate(connection, name))}
        />
      )}
    </FormDialog>
  )
}
