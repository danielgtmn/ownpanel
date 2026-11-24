import { Button } from '@ui-sdk/react/button'
import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Textarea } from '@ui-sdk/react/textarea'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { domainsToText, MAX_NAME_LENGTH, parseDomainInput, validateDomains, validateSiteName } from './validation.ts'

type SiteFormDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  mode: 'create' | 'edit'
  initialName?: string
  initialDomains?: string[]
  takenNames?: string[]
  onSubmit: (name: string, domains: string[]) => Promise<void>
}

type FormErrors = { name?: string; domains?: string; form?: string }

function routeServerError(message: string): FormErrors {
  const text = message.toLowerCase()
  if (text.includes('domain') || text.includes('host')) return { domains: message }
  if (text.includes('name')) return { name: message }
  return { form: message }
}

function SiteForm({
  mode,
  initialName = '',
  initialDomains = [],
  takenNames = [],
  onSubmit,
}: Omit<SiteFormDialogProps, 'open' | 'onOpenChange'>) {
  const [name, setName] = useState(initialName)
  const [domainText, setDomainText] = useState(domainsToText(initialDomains))
  const [errors, setErrors] = useState<FormErrors>({})
  const [busy, setBusy] = useState(false)
  const creating = mode === 'create'

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const domains = parseDomainInput(domainText)
    const next: FormErrors = {}
    if (creating) {
      const nameError = validateSiteName(name)
      if (nameError) next.name = nameError
      else if (takenNames.includes(name.trim())) next.name = 'You already have a site with this name.'
    }
    const domainError = validateDomains(domains)
    if (domainError) next.domains = domainError
    if (next.name || next.domains) {
      setErrors(next)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(name.trim(), domains)
    } catch (error) {
      setErrors(routeServerError(errorMessage(error)))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      {creating ? (
        <Field name="name" invalid={errors.name ? true : undefined}>
          <FieldLabel>Site name</FieldLabel>
          <Input
            value={name}
            maxLength={MAX_NAME_LENGTH}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="my-site"
            onValueChange={(value) => {
              setName(value)
              setErrors((current) => ({ ...current, name: undefined, form: undefined }))
            }}
          />
          <FieldDescription>Lowercase letters, numbers and hyphens. You can't change it later.</FieldDescription>
          {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
        </Field>
      ) : null}
      <Field name="domains" invalid={errors.domains ? true : undefined}>
        <FieldLabel>Domains</FieldLabel>
        <Textarea
          value={domainText}
          rows={4}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder={'example.com\nwww.example.com'}
          onValueChange={(value) => {
            setDomainText(value)
            setErrors((current) => ({ ...current, domains: undefined, form: undefined }))
          }}
        />
        <FieldDescription>One domain per line. Leave empty to add them later.</FieldDescription>
        {errors.domains ? <FieldError match>{errors.domains}</FieldError> : null}
      </Field>
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : creating ? 'Create site' : 'Save domains'}
        </Button>
      </div>
    </form>
  )
}

export function SiteFormDialog({ open, onOpenChange, ...formProps }: SiteFormDialogProps) {
  const [session, setSession] = useState(0)
  const [wasOpen, setWasOpen] = useState(open)
  if (open !== wasOpen) {
    setWasOpen(open)
    if (open) setSession((count) => count + 1)
  }
  const creating = formProps.mode === 'create'

  return (
    <Dialog open={open} onOpenChange={(next) => onOpenChange(next)}>
      <DialogBackdrop />
      <DialogContent>
        <div className="dialog-head">
          <DialogTitle>{creating ? 'New site' : 'Edit domains'}</DialogTitle>
          <DialogDescription>
            {creating
              ? 'Give your site a name and the domains visitors will use to reach it.'
              : 'Visitors can reach this site on these domains.'}
          </DialogDescription>
        </div>
        <SiteForm key={session} {...formProps} />
      </DialogContent>
    </Dialog>
  )
}
