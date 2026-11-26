import { Button } from '@ui-sdk/react/button'
import { Checkbox, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input, InputGroup } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { Textarea } from '@ui-sdk/react/textarea'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import { parseTargets, SENDER_POLICIES, validateLocalPart } from './rules.ts'
import type { AliasOptions, MailAlias, SenderPolicy } from './types.ts'

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
  onSubmit: (localPart: string, targets: string[], options: AliasOptions) => Promise<void>
}) {
  const [scope, setScope] = useState<Scope>(existing?.catch_all ? 'all' : 'one')
  const [local, setLocal] = useState(existing && !existing.catch_all ? existing.local_part : '')
  const [targetsText, setTargetsText] = useState(existing ? existing.targets.join('\n') : '')
  const [policy, setPolicy] = useState<SenderPolicy>(existing?.sender_policy ?? 'anyone')
  const [hidden, setHidden] = useState(existing?.hide_from_directory ?? false)
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
      // Mail to every other address can't be limited to some senders.
      const options: AliasOptions =
        scope === 'all' ? { sender_policy: 'anyone', hide_from_directory: false } : { sender_policy: policy, hide_from_directory: hidden }
      await onSubmit(scope === 'all' ? '*' : local.trim().toLowerCase(), targets, options)
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
          <InputGroup className="address-input">
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
            <span className="address-suffix" aria-hidden="true">
              @{domain}
            </span>
          </InputGroup>
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
      {scope === 'one' ? (
        <>
          <Field>
            <FieldLabel>Who may write to this address</FieldLabel>
            <RadioGroup
              value={policy}
              onValueChange={(value) => {
                const found = SENDER_POLICIES.find((option) => option.value === value)
                if (found) setPolicy(found.value)
              }}
            >
              {SENDER_POLICIES.map((option) => (
                <Field key={option.value}>
                  <Radio value={option.value}>
                    <RadioIndicator />
                  </Radio>
                  <div className="choice-text">
                    <FieldLabel>{option.label}</FieldLabel>
                    <FieldDescription>{option.hint}</FieldDescription>
                  </div>
                </Field>
              ))}
            </RadioGroup>
          </Field>
          <Field className="mail-choice">
            <Checkbox checked={hidden} onCheckedChange={(next) => setHidden(next === true)}>
              <CheckboxIndicator />
            </Checkbox>
            <div className="choice-text">
              <FieldLabel>Hide from the address book</FieldLabel>
              <FieldDescription>
                People of the organisation don’t see this address when they look up addresses. Mail still arrives.
              </FieldDescription>
            </div>
          </Field>
        </>
      ) : null}
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
  onSubmit: (localPart: string, targets: string[], options: AliasOptions) => Promise<void>
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
