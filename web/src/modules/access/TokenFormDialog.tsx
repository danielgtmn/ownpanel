import { Button } from '@ui-sdk/react/button'
import { Checkbox, CheckboxGroup, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { useState, type FormEvent } from 'react'
import { errorMessage, listSiteNames } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import {
  DEFAULT_EXPIRY,
  EXPIRY_OPTIONS,
  toNewToken,
  validateTokenDraft,
  type SiteScope,
  type TokenDraftErrors,
  type TokenExpiry,
} from './rules.ts'
import type { NewToken, TokenAccess } from './types.ts'

type FormErrors = TokenDraftErrors & { form?: string }

function TokenForm({ onSubmit }: { onSubmit: (token: NewToken) => Promise<void> }) {
  const sites = useResource(listSiteNames, 'site-names')
  const [name, setName] = useState('')
  const [access, setAccess] = useState<TokenAccess>('read')
  const [scope, setScope] = useState<SiteScope>('all')
  const [picked, setPicked] = useState<string[]>([])
  const [expiry, setExpiry] = useState<TokenExpiry>(DEFAULT_EXPIRY)
  const [errors, setErrors] = useState<FormErrors>({})
  const [busy, setBusy] = useState(false)
  const siteNames = sites.status === 'ready' ? sites.data : []

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const draft = { name, access, scope, picked, expiry }
    const found = validateTokenDraft(draft)
    if (found.name || found.sites) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(toNewToken(draft, siteNames))
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Token name</FieldLabel>
        <Input
          value={name}
          autoComplete="off"
          spellCheck={false}
          placeholder="Deploy script"
          autoFocus
          onValueChange={(value) => {
            setName(value)
            setErrors((current) => ({ ...current, name: undefined, form: undefined }))
          }}
        />
        <FieldDescription>Helps you remember which program uses it.</FieldDescription>
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>

      <Field>
        <FieldLabel>What it may do</FieldLabel>
        <RadioGroup value={access} onValueChange={(value) => value && setAccess(value as TokenAccess)}>
          <Field>
            <Radio value="read">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Read only</FieldLabel>
              <FieldDescription>Can look at your sites but can't change anything.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="write">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Read and change</FieldLabel>
              <FieldDescription>Can also publish new files, change domains and switch versions.</FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>

      <Field>
        <FieldLabel>Which sites</FieldLabel>
        <RadioGroup
          value={scope}
          onValueChange={(value) => {
            if (!value) return
            setScope(value as SiteScope)
            setErrors((current) => ({ ...current, sites: undefined }))
          }}
        >
          <Field>
            <Radio value="all">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>All my sites</FieldLabel>
              <FieldDescription>Includes sites you create later. Can also create and delete sites.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="specific">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Only specific sites</FieldLabel>
              <FieldDescription>
                A token limited to specific sites cannot create or delete sites.
              </FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>

      {scope === 'specific' ? (
        <Field invalid={errors.sites ? true : undefined}>
          <FieldLabel>Sites this token may reach</FieldLabel>
          {sites.status === 'loading' ? <p className="muted">Loading your sites…</p> : null}
          {sites.status === 'error' ? <p className="form-error">{errorMessage(sites.error)}</p> : null}
          {sites.status === 'ready' && siteNames.length === 0 ? (
            <p className="muted">You don't have any sites yet.</p>
          ) : null}
          {siteNames.length > 0 ? (
            <CheckboxGroup
              className="site-picker"
              value={picked}
              onValueChange={(value) => {
                setPicked(value)
                setErrors((current) => ({ ...current, sites: undefined }))
              }}
            >
              {siteNames.map((site) => (
                <label data-slot="label" key={site}>
                  <Checkbox value={site}>
                    <CheckboxIndicator />
                  </Checkbox>
                  {site}
                </label>
              ))}
            </CheckboxGroup>
          ) : null}
          {errors.sites ? <FieldError match>{errors.sites}</FieldError> : null}
        </Field>
      ) : null}

      <Field name="expiry">
        <FieldLabel>Stops working after</FieldLabel>
        <Select value={expiry} onValueChange={(value) => value && setExpiry(value as TokenExpiry)}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {EXPIRY_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FieldDescription>
          {expiry === 'never'
            ? 'Works until you delete it. A token that expires limits the damage if it leaks.'
            : 'After that, programs using it are turned away. Create a new token then.'}
        </FieldDescription>
      </Field>

      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Creating…' : 'Create token'}
        </Button>
      </div>
    </form>
  )
}

type TokenFormDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (token: NewToken) => Promise<void>
}

export function TokenFormDialog({ open, onOpenChange, onSubmit }: TokenFormDialogProps) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="New access token"
      description="Create a token for a program or script. You'll see it once, right after you create it."
    >
      <TokenForm onSubmit={onSubmit} />
    </FormDialog>
  )
}
