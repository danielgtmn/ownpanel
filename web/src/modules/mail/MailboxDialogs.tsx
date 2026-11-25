import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import {
  NumberInput,
  NumberInputControl,
  NumberInputDecrement,
  NumberInputGroup,
  NumberInputIncrement,
} from '@ui-sdk/react/number-input'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { PASSWORD_MIN, validatePasswordPair } from '../../shared/session/index.ts'
import { FormDialog, PasswordField } from '../../shared/ui/index.ts'
import { bytesToGb, limitsChange, validateLocalPart, type LimitsErrors } from './rules.ts'
import type { LimitsChange, Mailbox } from './types.ts'

function FormError({ message }: { message?: string }) {
  return message ? (
    <p className="form-error" role="alert">
      {message}
    </p>
  ) : null
}

type PasswordErrors = { password?: string; repeat?: string }

function PasswordPair({
  password,
  repeat,
  errors,
  onPassword,
  onRepeat,
  autoFocus,
}: {
  password: string
  repeat: string
  errors: PasswordErrors
  onPassword: (value: string) => void
  onRepeat: (value: string) => void
  autoFocus?: boolean
}) {
  return (
    <>
      <PasswordField
        name="password"
        label="Password"
        autoComplete="new-password"
        value={password}
        description={`At least ${PASSWORD_MIN} characters. It goes into the mail program together with the address.`}
        error={errors.password}
        autoFocus={autoFocus}
        onValueChange={onPassword}
      />
      <PasswordField
        name="repeat"
        label="Repeat password"
        autoComplete="new-password"
        value={repeat}
        error={errors.repeat}
        onValueChange={onRepeat}
      />
    </>
  )
}

// ---- create ----

type CreateErrors = PasswordErrors & { local?: string; form?: string }

function CreateMailboxForm({
  domain,
  onSubmit,
}: {
  domain: string
  onSubmit: (localPart: string, password: string) => Promise<void>
}) {
  const [local, setLocal] = useState('')
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [errors, setErrors] = useState<CreateErrors>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found: CreateErrors = { ...validatePasswordPair(password, repeat) }
    const localError = validateLocalPart(local)
    if (localError) found.local = localError
    if (found.local || found.password || found.repeat) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(local.trim().toLowerCase(), password)
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="local_part" invalid={errors.local ? true : undefined}>
        <FieldLabel>Address</FieldLabel>
        <div className="mail-address-input">
          <Input
            value={local}
            placeholder="info"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            autoFocus
            onValueChange={(value) => {
              setLocal(value)
              setErrors((current) => ({ ...current, local: undefined, form: undefined }))
            }}
          />
          <span className="mail-address-domain" aria-hidden="true">
            @{domain}
          </span>
        </div>
        <FieldDescription>The part before @{domain}. Letters, digits and . - _ +</FieldDescription>
        {errors.local ? <FieldError match>{errors.local}</FieldError> : null}
      </Field>
      <PasswordPair
        password={password}
        repeat={repeat}
        errors={errors}
        onPassword={(value) => {
          setPassword(value)
          setErrors((current) => ({ ...current, password: undefined, form: undefined }))
        }}
        onRepeat={(value) => {
          setRepeat(value)
          setErrors((current) => ({ ...current, repeat: undefined, form: undefined }))
        }}
      />
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Creating…' : 'Create mailbox'}
        </Button>
      </div>
    </form>
  )
}

export function CreateMailboxDialog({
  open,
  onOpenChange,
  domain,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  domain: string
  onSubmit: (localPart: string, password: string) => Promise<void>
}) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Create mailbox"
      description={`A mailbox stores the mail for one address on ${domain} and can send from it.`}
    >
      <CreateMailboxForm domain={domain} onSubmit={onSubmit} />
    </FormDialog>
  )
}

// ---- password ----

function PasswordForm({ onSubmit }: { onSubmit: (password: string) => Promise<void> }) {
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [errors, setErrors] = useState<PasswordErrors & { form?: string }>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validatePasswordPair(password, repeat)
    if (found.password || found.repeat) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(password)
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <PasswordPair
        password={password}
        repeat={repeat}
        errors={errors}
        autoFocus
        onPassword={(value) => {
          setPassword(value)
          setErrors((current) => ({ ...current, password: undefined, form: undefined }))
        }}
        onRepeat={(value) => {
          setRepeat(value)
          setErrors((current) => ({ ...current, repeat: undefined, form: undefined }))
        }}
      />
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Set password'}
        </Button>
      </div>
    </form>
  )
}

export function MailboxPasswordDialog({
  mailbox,
  onClose,
  onSubmit,
}: {
  mailbox: Mailbox | null
  onClose: () => void
  onSubmit: (password: string) => Promise<void>
}) {
  const [shown, setShown] = useState(mailbox)
  if (mailbox && mailbox !== shown) setShown(mailbox)
  return (
    <FormDialog
      open={mailbox !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title="Set a new password"
      description={`Mail programs signed in to ${shown?.address ?? 'this mailbox'} have to sign in again with the new password.`}
    >
      <PasswordForm onSubmit={onSubmit} />
    </FormDialog>
  )
}

// ---- limits ----

function CountField({
  label,
  name,
  value,
  error,
  description,
  onValueChange,
}: {
  label: string
  name: string
  value: number | null
  error?: string
  description: string
  onValueChange: (value: number | null) => void
}) {
  return (
    <Field invalid={error ? true : undefined}>
      <FieldLabel>{label}</FieldLabel>
      <NumberInput name={name} value={value} min={1} format={{ useGrouping: false }} onValueChange={onValueChange}>
        <NumberInputGroup>
          <NumberInputDecrement aria-label={`Fewer ${label.toLowerCase()}`} />
          <NumberInputControl />
          <NumberInputIncrement aria-label={`More ${label.toLowerCase()}`} />
        </NumberInputGroup>
      </NumberInput>
      <FieldDescription>{description}</FieldDescription>
      {error ? <FieldError match>{error}</FieldError> : null}
    </Field>
  )
}

function LimitsForm({
  mailbox,
  isAdmin,
  onSubmit,
}: {
  mailbox: Mailbox
  isAdmin: boolean
  onSubmit: (change: LimitsChange) => Promise<void>
}) {
  const [quotaGb, setQuotaGb] = useState(bytesToGb(mailbox.quota_bytes))
  const [perHour, setPerHour] = useState<number | null>(mailbox.send_per_hour)
  const [perDay, setPerDay] = useState<number | null>(mailbox.send_per_day)
  const [errors, setErrors] = useState<LimitsErrors & { form?: string }>({})
  const [busy, setBusy] = useState(false)
  const lowerOnly = isAdmin ? '' : ' You can lower it; an administrator can raise it.'

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const { change, errors: found } = limitsChange(mailbox, { quotaGb, perHour, perDay }, isAdmin)
    if (found.quota || found.perHour || found.perDay) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(change)
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="quota" invalid={errors.quota ? true : undefined}>
        <FieldLabel>Storage (GB)</FieldLabel>
        <Input
          value={quotaGb}
          inputMode="decimal"
          autoComplete="off"
          placeholder={isAdmin ? 'No limit' : undefined}
          onValueChange={(value) => {
            setQuotaGb(value)
            setErrors((current) => ({ ...current, quota: undefined, form: undefined }))
          }}
        />
        <FieldDescription>
          How much mail the mailbox may keep. When it is full, new mail is turned away.
          {isAdmin ? ' Leave empty for no limit.' : lowerOnly}
        </FieldDescription>
        {errors.quota ? <FieldError match>{errors.quota}</FieldError> : null}
      </Field>
      <CountField
        label="Messages per hour"
        name="send_per_hour"
        value={perHour}
        error={errors.perHour}
        description={`How many messages the mailbox may send in an hour.${lowerOnly}`}
        onValueChange={(value) => {
          setPerHour(value)
          setErrors((current) => ({ ...current, perHour: undefined, form: undefined }))
        }}
      />
      <CountField
        label="Messages per day"
        name="send_per_day"
        value={perDay}
        error={errors.perDay}
        description={`How many messages the mailbox may send in a day.${lowerOnly}`}
        onValueChange={(value) => {
          setPerDay(value)
          setErrors((current) => ({ ...current, perDay: undefined, form: undefined }))
        }}
      />
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Save limits'}
        </Button>
      </div>
    </form>
  )
}

export function MailboxLimitsDialog({
  mailbox,
  isAdmin,
  onClose,
  onSubmit,
}: {
  mailbox: Mailbox | null
  isAdmin: boolean
  onClose: () => void
  onSubmit: (change: LimitsChange) => Promise<void>
}) {
  const [shown, setShown] = useState(mailbox)
  if (mailbox && mailbox !== shown) setShown(mailbox)
  return (
    <FormDialog
      open={mailbox !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title="Change limits"
      description={`Storage and sending limits of ${shown?.address ?? 'the mailbox'}.`}
    >
      {shown ? <LimitsForm mailbox={shown} isAdmin={isAdmin} onSubmit={onSubmit} /> : null}
    </FormDialog>
  )
}
