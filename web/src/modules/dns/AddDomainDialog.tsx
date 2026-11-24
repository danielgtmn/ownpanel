import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import { Nameservers } from './Nameservers.tsx'
import type { Connection, Zone } from './types.ts'
import { normalizeZoneName, validateZoneName } from './zones.ts'

function AddDomainForm({ onSubmit }: { onSubmit: (name: string) => Promise<void> }) {
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const problem = validateZoneName(name)
    if (problem) {
      setError(problem)
      return
    }
    setBusy(true)
    try {
      await onSubmit(normalizeZoneName(name))
    } catch (caught) {
      setError(errorMessage(caught))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="domain" invalid={error ? true : undefined}>
        <FieldLabel>Domain</FieldLabel>
        <Input
          value={name}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder="example.com"
          autoFocus
          onValueChange={(value) => {
            setName(value)
            setError(null)
          }}
        />
        <FieldDescription>The domain you registered, like example.com, without www.</FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Adding…' : 'Add domain'}
        </Button>
      </div>
    </form>
  )
}

function Added({ zone }: { zone: Zone }) {
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

type Props = {
  connection: Connection | null
  onOpenChange: (open: boolean) => void
  onCreate: (connection: Connection, name: string) => Promise<Zone>
}

export function AddDomainDialog({ connection, onOpenChange, onCreate }: Props) {
  const [created, setCreated] = useState<Zone | null>(null)
  const open = connection !== null

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
          ? `${created.name} is now at ${created.connection_name}.`
          : `Create a domain at ${connection?.name ?? 'Hetzner'} so you can manage its DNS records here.`
      }
    >
      {created ? (
        <Added zone={created} />
      ) : (
        <AddDomainForm
          onSubmit={async (name) => {
            if (connection) setCreated(await onCreate(connection, name))
          }}
        />
      )}
    </FormDialog>
  )
}
