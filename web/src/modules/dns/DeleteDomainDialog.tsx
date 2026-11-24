import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import { canConfirmDelete, usesFromMessage } from './zones.ts'

export function DeleteDomainForm({ zone, onDelete }: { zone: string; onDelete: (confirm: string) => Promise<void> }) {
  const [typed, setTyped] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const matches = canConfirmDelete(typed, zone)
  const uses = error ? usesFromMessage(error) : []

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!matches) return
    setBusy(true)
    setError(null)
    try {
      await onDelete(typed.trim())
    } catch (caught) {
      setError(errorMessage(caught))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="confirm">
        <FieldLabel>Type {zone} to confirm</FieldLabel>
        <Input
          value={typed}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          autoFocus
          onValueChange={(value) => {
            setTyped(value)
            setError(null)
          }}
        />
        <FieldDescription>The Delete button works once the name matches.</FieldDescription>
      </Field>
      {error ? (
        <div className="form-error" role="alert">
          {uses.length > 0 ? (
            <>
              <p>{zone} is still used by:</p>
              <ul className="domain-uses">
                {uses.map((use) => (
                  <li key={use}>{use}</li>
                ))}
              </ul>
              <p>Remove those first, then try again.</p>
            </>
          ) : (
            <p>{error}</p>
          )}
        </div>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="destructive" disabled={!matches || busy}>
          {busy ? 'Deleting…' : 'Delete domain'}
        </Button>
      </div>
    </form>
  )
}

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  zone: string
  connectionName: string
  onDelete: (confirm: string) => Promise<void>
}

export function DeleteDomainDialog({ open, onOpenChange, zone, connectionName, onDelete }: Props) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Delete ${zone}?`}
      description={`The domain and all its DNS records are deleted at ${connectionName}. Websites and email on it stop working. This can't be undone.`}
    >
      <DeleteDomainForm zone={zone} onDelete={onDelete} />
    </FormDialog>
  )
}
