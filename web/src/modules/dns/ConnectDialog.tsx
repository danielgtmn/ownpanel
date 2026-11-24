import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog, PasswordField } from '../../shared/ui/index.ts'
import { validateConnection } from './zones.ts'
import type { NewConnection } from './types.ts'

type FormErrors = { name?: string; token?: string; form?: string }

function ConnectForm({ onSubmit }: { onSubmit: (connection: NewConnection) => Promise<void> }) {
  const [name, setName] = useState('')
  const [token, setToken] = useState('')
  const [errors, setErrors] = useState<FormErrors>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateConnection(name, token)
    if (found.name || found.token) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit({ provider: 'hetzner', name: name.trim(), token: token.trim() })
    } catch (error) {
      const message = errorMessage(error)
      setErrors(/token/i.test(message) ? { token: message } : { form: message })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Name</FieldLabel>
        <Input
          value={name}
          autoComplete="off"
          spellCheck={false}
          placeholder="My Hetzner project"
          autoFocus
          onValueChange={(value) => {
            setName(value)
            setErrors((current) => ({ ...current, name: undefined, form: undefined }))
          }}
        />
        <FieldDescription>Helps you recognize this connection.</FieldDescription>
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>
      <PasswordField
        name="token"
        label="API token"
        value={token}
        autoComplete="new-password"
        error={errors.token}
        description="Create it in the Hetzner Console under the project that holds your domains: Security → API tokens, with “Read & Write” permission. It is stored encrypted on this server and never shown again."
        onValueChange={(value) => {
          setToken(value)
          setErrors((current) => ({ ...current, token: undefined, form: undefined }))
        }}
      />
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Connecting…' : 'Connect'}
        </Button>
      </div>
    </form>
  )
}

type ConnectDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (connection: NewConnection) => Promise<void>
}

export function ConnectDialog({ open, onOpenChange, onSubmit }: ConnectDialogProps) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Connect Hetzner"
      description="Connect your Hetzner account so you can manage your domains' DNS records here."
    >
      <ConnectForm onSubmit={onSubmit} />
    </FormDialog>
  )
}
