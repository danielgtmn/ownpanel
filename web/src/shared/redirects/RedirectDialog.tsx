import { Button } from '@ui-sdk/react/button'
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from '@ui-sdk/react/collapsible'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { useState, type FormEvent, type ReactNode } from 'react'
import { errorMessage, type Redirect, type RedirectInput } from '../api/index.ts'
import { FormDialog } from '../ui/index.ts'
import {
  codeFor,
  draftFrom,
  hasRedirectErrors,
  inputFrom,
  isPrefixPath,
  routeRedirectError,
  validateRedirect,
  type RedirectDraft,
  type RedirectErrors,
  type RedirectKind,
} from './rules.ts'

type FormErrors = RedirectErrors & { form?: string }

type FormProps = {
  kind: RedirectKind
  existing: Redirect | null
  domains: string[]
  onSubmit: (input: RedirectInput) => Promise<void>
}

function SwitchField({
  label,
  description,
  checked,
  onCheckedChange,
}: {
  label: string
  description: ReactNode
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <Field className="switch-field">
      <Switch checked={checked} onCheckedChange={(next) => onCheckedChange(next === true)}>
        <SwitchThumb />
      </Switch>
      <div className="choice-text">
        <FieldLabel>{label}</FieldLabel>
        <FieldDescription>{description}</FieldDescription>
      </div>
    </Field>
  )
}

function RedirectForm({ kind, existing, domains, onSubmit }: FormProps) {
  const [draft, setDraft] = useState<RedirectDraft>(() =>
    draftFrom(existing, kind === 'path' ? { domain: domains[0] ?? '', keepPath: false, keepQuery: false } : {}),
  )
  const [errors, setErrors] = useState<FormErrors>({})
  const [busy, setBusy] = useState(false)
  const showKeepPath = kind === 'domain' || isPrefixPath(draft.path)
  const code = codeFor(draft.permanent, draft.keepMethod)

  function change(patch: Partial<RedirectDraft>, field?: keyof RedirectErrors) {
    setDraft((current) => ({ ...current, ...patch }))
    setErrors((current) => (field ? { ...current, [field]: undefined, form: undefined } : { ...current, form: undefined }))
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateRedirect(draft, kind)
    if (hasRedirectErrors(found)) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(inputFrom(draft, kind))
    } catch (error) {
      setErrors(routeRedirectError(errorMessage(error), kind))
      setBusy(false)
    }
  }

  const exampleFrom = kind === 'domain' ? draft.domain.trim() || 'old.example.com' : `${draft.domain}${draft.path.replace(/\*$/, '') || '/blog/'}`

  return (
    <form className="form" onSubmit={submit} noValidate>
      {kind === 'path' ? (
        <Field name="domain" invalid={errors.domain ? true : undefined}>
          <FieldLabel>Domain</FieldLabel>
          <Select
            value={draft.domain}
            onValueChange={(value) => {
              if (value) change({ domain: value }, 'domain')
            }}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {domains.map((domain) => (
                <SelectItem key={domain} value={domain}>
                  {domain}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {errors.domain ? <FieldError match>{errors.domain}</FieldError> : null}
        </Field>
      ) : (
        <Field name="domain" invalid={errors.domain ? true : undefined}>
          <FieldLabel>Domain to redirect</FieldLabel>
          <Input
            value={draft.domain}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="old.example.com"
            autoFocus={!existing}
            onValueChange={(value) => change({ domain: value }, 'domain')}
          />
          <FieldDescription>Everyone who opens this domain is sent on. It can't be a domain of a site.</FieldDescription>
          {errors.domain ? <FieldError match>{errors.domain}</FieldError> : null}
        </Field>
      )}

      {kind === 'path' ? (
        <Field name="path" invalid={errors.path ? true : undefined}>
          <FieldLabel>Path</FieldLabel>
          <Input
            value={draft.path}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="/old"
            autoFocus={!existing}
            onValueChange={(value) => change({ path: value }, 'path')}
          />
          <FieldDescription>
            An exact path like /old, or end it with /* for everything below it, like /blog/*. Upper and lower case
            matter.
          </FieldDescription>
          {errors.path ? <FieldError match>{errors.path}</FieldError> : null}
        </Field>
      ) : null}

      <Field name="target" invalid={errors.target ? true : undefined}>
        <FieldLabel>Send visitors to</FieldLabel>
        <Input
          value={draft.target}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder={kind === 'path' ? '/new' : 'https://example.com'}
          onValueChange={(value) => change({ target: value }, 'target')}
        />
        <FieldDescription>
          {kind === 'path'
            ? 'A path on the same domain like /new, or a full address like https://example.com/page.'
            : 'A full address like https://example.com.'}
        </FieldDescription>
        {errors.target ? <FieldError match>{errors.target}</FieldError> : null}
      </Field>

      <Field>
        <FieldLabel>Type</FieldLabel>
        <RadioGroup
          value={draft.permanent ? 'permanent' : 'temporary'}
          onValueChange={(value) => value && change({ permanent: value === 'permanent' })}
        >
          <Field>
            <Radio value="permanent">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Permanent ({draft.keepMethod ? 308 : 301})</FieldLabel>
              <FieldDescription>Browsers and search engines remember the new address.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="temporary">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Temporary ({draft.keepMethod ? 307 : 302})</FieldLabel>
              <FieldDescription>For a while only. Browsers keep asking the old address.</FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>

      {showKeepPath ? (
        <SwitchField
          label="Keep the rest of the path"
          description={`${exampleFrom}/a/b leads to the target followed by /a/b.`}
          checked={draft.keepPath}
          onCheckedChange={(checked) => change({ keepPath: checked }, 'target')}
        />
      ) : null}
      <SwitchField
        label="Keep the query"
        description="Anything after ? in the address, like ?page=2, is passed on."
        checked={draft.keepQuery}
        onCheckedChange={(checked) => change({ keepQuery: checked }, 'target')}
      />

      <Collapsible className="redirect-advanced" defaultOpen={draft.keepMethod}>
        <CollapsibleTrigger>Advanced</CollapsibleTrigger>
        <CollapsiblePanel>
          <SwitchField
            label="Keep the method"
            description={`Forms sent to the old address are sent the same way to the new one. Uses ${code === 308 || code === 301 ? '308 instead of 301' : '307 instead of 302'}.`}
            checked={draft.keepMethod}
            onCheckedChange={(checked) => change({ keepMethod: checked })}
          />
        </CollapsiblePanel>
      </Collapsible>

      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : existing ? 'Save redirect' : 'Add redirect'}
        </Button>
      </div>
    </form>
  )
}

type RedirectDialogProps = FormProps & {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function RedirectDialog({ open, onOpenChange, kind, existing, domains, onSubmit }: RedirectDialogProps) {
  const description =
    kind === 'path'
      ? 'Send visitors who open one path of this site to another path or address.'
      : 'Send everyone who opens a domain to another address.'
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={existing ? 'Edit redirect' : 'Add redirect'}
      description={description}
    >
      <RedirectForm kind={kind} existing={existing} domains={domains} onSubmit={onSubmit} />
    </FormDialog>
  )
}
