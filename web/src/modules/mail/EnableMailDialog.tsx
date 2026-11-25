import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import { validateMailDomain } from './rules.ts'

function EnableMailForm({ initial, onSubmit }: { initial: string; onSubmit: (domain: string) => Promise<void> }) {
  const [domain, setDomain] = useState(initial)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const problem = validateMailDomain(domain)
    if (problem) {
      setError(problem)
      return
    }
    setBusy(true)
    setError(null)
    try {
      await onSubmit(domain.trim().toLowerCase().replace(/\.$/, ''))
    } catch (failure) {
      setError(errorMessage(failure))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="domain" invalid={error ? true : undefined}>
        <FieldLabel>Domain</FieldLabel>
        <Input
          value={domain}
          placeholder="example.com"
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          autoFocus
          onValueChange={(value) => {
            setDomain(value)
            setError(null)
          }}
        />
        <FieldDescription>
          Addresses end in @ this domain, for example info@{domain.trim() || 'example.com'}. It has to be a domain of one of
          your sites or redirects, or one of your DNS connections.
        </FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Turning on…' : 'Turn on e-mail'}
        </Button>
      </div>
    </form>
  )
}

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  initialDomain?: string
  onSubmit: (domain: string) => Promise<void>
}

export function EnableMailDialog({ open, onOpenChange, initialDomain, onSubmit }: Props) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Turn on e-mail"
      description="Mailboxes and forwarding for a domain. Mail arrives here once the domain's mail records point to this server."
    >
      <EnableMailForm initial={initialDomain ?? ''} onSubmit={onSubmit} />
    </FormDialog>
  )
}
