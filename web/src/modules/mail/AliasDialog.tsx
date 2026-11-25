import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { Textarea } from '@ui-sdk/react/textarea'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import { parseTargets, validateLocalPart } from './rules.ts'
import type { MailAlias } from './types.ts'

type Errors = { local?: string; targets?: string; form?: string }
type Scope = 'one' | 'all'

function AliasForm({
  domain,
  existing,
  catchAllTaken,
  onSubmit,
}: {
  domain: string
  existing: MailAlias | null
  catchAllTaken: boolean
  onSubmit: (localPart: string, targets: string[]) => Promise<void>
}) {
  const [scope, setScope] = useState<Scope>(existing?.catch_all ? 'all' : 'one')
  const [local, setLocal] = useState(existing && !existing.catch_all ? existing.local_part : '')
  const [targetsText, setTargetsText] = useState(existing ? existing.targets.join('\n') : '')
  const [errors, setErrors] = useState<Errors>({})
  const [busy, setBusy] = useState(false)
  const allDisabled = catchAllTaken && !existing?.catch_all

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found: Errors = {}
    if (scope === 'one') {
      const problem = validateLocalPart(local)
      if (problem) found.local = problem
    }
    const { targets, error } = parseTargets(targetsText)
    if (error) found.targets = error
    if (found.local || found.targets) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(scope === 'all' ? '*' : local.trim().toLowerCase(), targets)
    } catch (failure) {
      setErrors({ form: errorMessage(failure) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field>
        <FieldLabel>Mail sent to</FieldLabel>
        <RadioGroup value={scope} onValueChange={(value) => value && setScope(value as Scope)}>
          <Field>
            <Radio value="one">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>One address</FieldLabel>
              <FieldDescription>For example sales@{domain}.</FieldDescription>
            </div>
          </Field>
          <Field disabled={allDisabled}>
            <Radio value="all" disabled={allDisabled}>
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>All other addresses</FieldLabel>
              <FieldDescription>
                {allDisabled
                  ? 'There is one for this domain already; edit that one instead.'
                  : `Every address on ${domain} that has no mailbox or forwarding of its own.`}
              </FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>
      {scope === 'one' ? (
        <Field name="local_part" invalid={errors.local ? true : undefined}>
          <FieldLabel>Address</FieldLabel>
          <div className="mail-address-input">
            <Input
              value={local}
              placeholder="sales"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              onValueChange={(value) => {
                setLocal(value)
                setErrors((current) => ({ ...current, local: undefined, form: undefined }))
              }}
            />
            <span className="mail-address-domain" aria-hidden="true">
              @{domain}
            </span>
          </div>
          {errors.local ? <FieldError match>{errors.local}</FieldError> : null}
        </Field>
      ) : null}
      <Field name="targets" invalid={errors.targets ? true : undefined}>
        <FieldLabel>Deliver to</FieldLabel>
        <Textarea
          value={targetsText}
          rows={3}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder={`info@${domain}`}
          onValueChange={(value) => {
            setTargetsText(value)
            setErrors((current) => ({ ...current, targets: undefined, form: undefined }))
          }}
        />
        <FieldDescription>One address per line: mailboxes here or addresses anywhere else. Up to 50.</FieldDescription>
        {errors.targets ? <FieldError match>{errors.targets}</FieldError> : null}
      </Field>
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : existing ? 'Save' : 'Add forwarding'}
        </Button>
      </div>
    </form>
  )
}

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  domain: string
  existing: MailAlias | null
  catchAllTaken: boolean
  onSubmit: (localPart: string, targets: string[]) => Promise<void>
}

export function AliasDialog({ open, onOpenChange, domain, existing, catchAllTaken, onSubmit }: Props) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={existing ? 'Edit forwarding' : 'Add forwarding'}
      description="Mail to an address without a mailbox of its own goes on to the addresses you list."
    >
      <AliasForm domain={domain} existing={existing} catchAllTaken={catchAllTaken} onSubmit={onSubmit} />
    </FormDialog>
  )
}
