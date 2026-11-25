import { Button } from '@ui-sdk/react/button'
import { Checkbox, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { Dialog, DialogBackdrop, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input, InputGroup } from '@ui-sdk/react/input'
import {
  NumberInput,
  NumberInputControl,
  NumberInputDecrement,
  NumberInputGroup,
  NumberInputIncrement,
} from '@ui-sdk/react/number-input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { PASSWORD_MIN, validatePasswordPair } from '../../shared/session/index.ts'
import { copyText, FormDialog, PasswordField } from '../../shared/ui/index.ts'
import {
  APP_PASSWORD_NAME_MAX,
  bytesToGb,
  DISPLAY_NAME_MAX,
  limitsChange,
  validateAppPasswordName,
  validateDisplayName,
  validateLocalPart,
  type LimitsErrors,
} from './rules.ts'
import type { LimitsChange, Mailbox, MailboxKind, NewMailbox } from './types.ts'

const SHARED_HINT =
  'For a team, like info@ or support@. Several people of the organisation read it and send from it with their own sign-in; nobody signs in to it directly.'

const DISPLAY_NAME_HINT = 'Shown as the sender’s name next to the address, for example “Anna Muster”.'
function DisplayNameField({
  value,
  error,
  onValueChange,
  autoFocus,
  description,
}: {
  value: string
  error?: string
  onValueChange: (value: string) => void
  autoFocus?: boolean
  description: string
}) {
  return (
    <Field name="display_name" invalid={error ? true : undefined}>
      <FieldLabel>Display name</FieldLabel>
      <Input
        value={value}
        maxLength={DISPLAY_NAME_MAX}
        autoComplete="off"
        autoFocus={autoFocus}
        onValueChange={onValueChange}
      />
      <FieldDescription>{description}</FieldDescription>
      {error ? <FieldError match>{error}</FieldError> : null}
    </Field>
  )
}

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

type CreateErrors = PasswordErrors & { local?: string; name?: string; form?: string }

function CreateMailboxForm({ domain, onSubmit }: { domain: string; onSubmit: (mailbox: NewMailbox) => Promise<void> }) {
  const [kind, setKind] = useState<MailboxKind>('personal')
  const [local, setLocal] = useState('')
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [keepSent, setKeepSent] = useState(false)
  const [errors, setErrors] = useState<CreateErrors>({})
  const [busy, setBusy] = useState(false)
  const shared = kind === 'shared'

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found: CreateErrors = shared ? {} : { ...validatePasswordPair(password, repeat) }
    const localError = validateLocalPart(local)
    if (localError) found.local = localError
    const nameError = validateDisplayName(name)
    if (nameError) found.name = nameError
    if (found.local || found.name || found.password || found.repeat) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(
        shared
          ? { local_part: local.trim().toLowerCase(), kind, display_name: name.trim(), keep_sent_copy: keepSent }
          : { local_part: local.trim().toLowerCase(), kind, display_name: name.trim(), password },
      )
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field>
        <FieldLabel>Kind of mailbox</FieldLabel>
        <RadioGroup
          value={kind}
          onValueChange={(value) => {
            if (value !== 'personal' && value !== 'shared') return
            setKind(value)
            setErrors((current) => ({ ...current, password: undefined, repeat: undefined, form: undefined }))
          }}
        >
          <Field>
            <Radio value="personal">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Personal mailbox</FieldLabel>
              <FieldDescription>For one person, who signs in to it with its password.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="shared">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Shared mailbox</FieldLabel>
              <FieldDescription>{SHARED_HINT}</FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>
      <Field name="local_part" invalid={errors.local ? true : undefined}>
        <FieldLabel>Address</FieldLabel>
        <InputGroup className="address-input">
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
          <span className="address-suffix" aria-hidden="true">
            @{domain}
          </span>
        </InputGroup>
        <FieldDescription>The part before @{domain}. Letters, digits and . - _ +</FieldDescription>
        {errors.local ? <FieldError match>{errors.local}</FieldError> : null}
      </Field>
      <DisplayNameField
        value={name}
        error={errors.name}
        description={shared ? 'Optional. Shown as the sender’s name, for example “Support team”.' : `Optional. ${DISPLAY_NAME_HINT}`}
        onValueChange={(value) => {
          setName(value)
          setErrors((current) => ({ ...current, name: undefined, form: undefined }))
        }}
      />
      {shared ? (
        <KeepSentCopyField checked={keepSent} onCheckedChange={setKeepSent} />
      ) : (
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
      )}
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

function KeepSentCopyField({
  checked,
  onCheckedChange,
}: {
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <Field className="mail-choice">
      <Checkbox checked={checked} onCheckedChange={(next) => onCheckedChange(next === true)}>
        <CheckboxIndicator />
      </Checkbox>
      <div className="choice-text">
        <FieldLabel>Keep a copy of mail sent for this mailbox in its Sent folder</FieldLabel>
        <FieldDescription>So everyone with access sees what was sent from it, whoever sent it.</FieldDescription>
      </div>
    </Field>
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
  onSubmit: (mailbox: NewMailbox) => Promise<void>
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

// ---- make a shared mailbox personal ----

export function MakePersonalDialog({
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
      title="Make it a personal mailbox"
      description={`Someone can then sign in to ${shown?.address ?? 'the mailbox'} directly with this password. The mail stays.`}
    >
      <PasswordForm submitLabel="Make personal" onSubmit={onSubmit} />
    </FormDialog>
  )
}

// ---- password ----

function PasswordForm({
  submitLabel = 'Set password',
  onSubmit,
}: {
  submitLabel?: string
  onSubmit: (password: string) => Promise<void>
}) {
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
          {busy ? 'Saving…' : submitLabel}
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

// ---- display name ----

function DisplayNameForm({ mailbox, onSubmit }: { mailbox: Mailbox; onSubmit: (name: string) => Promise<void> }) {
  const [name, setName] = useState(mailbox.display_name)
  const [errors, setErrors] = useState<{ name?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateDisplayName(name)
    if (found) {
      setErrors({ name: found })
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(name.trim())
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <DisplayNameField
        value={name}
        error={errors.name}
        autoFocus
        description={`${DISPLAY_NAME_HINT} Leave it empty to show only the address.`}
        onValueChange={(value) => {
          setName(value)
          setErrors({})
        }}
      />
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Save name'}
        </Button>
      </div>
    </form>
  )
}

export function DisplayNameDialog({
  mailbox,
  onClose,
  onSubmit,
}: {
  mailbox: Mailbox | null
  onClose: () => void
  onSubmit: (name: string) => Promise<void>
}) {
  const [shown, setShown] = useState(mailbox)
  if (mailbox && mailbox !== shown) setShown(mailbox)
  return (
    <FormDialog
      open={mailbox !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title="Change the display name"
      description={`The name people see with mail from ${shown?.address ?? 'this mailbox'}. Mail programs that set their own name keep using theirs.`}
    >
      {shown ? <DisplayNameForm mailbox={shown} onSubmit={onSubmit} /> : null}
    </FormDialog>
  )
}

// ---- app passwords ----

function AppPasswordForm({ onSubmit }: { onSubmit: (name: string) => Promise<void> }) {
  const [name, setName] = useState('')
  const [errors, setErrors] = useState<{ name?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateAppPasswordName(name)
    if (found) {
      setErrors({ name: found })
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(name.trim())
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Device or program</FieldLabel>
        <Input
          value={name}
          placeholder="Phone"
          maxLength={APP_PASSWORD_NAME_MAX}
          autoComplete="off"
          autoFocus
          onValueChange={(value) => {
            setName(value)
            setErrors({})
          }}
        />
        <FieldDescription>Where you will use it, so you can tell the app passwords apart later.</FieldDescription>
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Creating…' : 'Create app password'}
        </Button>
      </div>
    </form>
  )
}

export function CreateAppPasswordDialog({
  mailbox,
  onClose,
  onSubmit,
}: {
  mailbox: Mailbox | null
  onClose: () => void
  onSubmit: (name: string) => Promise<void>
}) {
  const [shown, setShown] = useState(mailbox)
  if (mailbox && mailbox !== shown) setShown(mailbox)
  return (
    <FormDialog
      open={mailbox !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title="Create app password"
      description={`A password of its own for one device or mail program, used with the address ${shown?.address ?? ''}. If the device is lost, revoke just this one.`}
    >
      <AppPasswordForm onSubmit={onSubmit} />
    </FormDialog>
  )
}

// NewAppPasswordDialog shows a new app password once. The password lives
// only in the caller's state, which drops it when the dialog closes.
export function NewAppPasswordDialog({
  created,
  onClose,
}: {
  created: { address: string; name: string; password: string } | null
  onClose: () => void
}) {
  const shown = created
  return (
    <Dialog
      open={created !== null}
      onOpenChange={(next, details) => {
        if (next) return
        if (details.reason === 'outside-press') {
          details.cancel()
          return
        }
        onClose()
      }}
    >
      <DialogBackdrop />
      <DialogContent>
        <div className="dialog-head">
          <DialogTitle>Your new app password</DialogTitle>
          <DialogDescription>
            Enter it in “{shown?.name}” as the password for {shown?.address}. Copy it now: it will not be shown again. If
            you lose it, revoke it and create a new one.
          </DialogDescription>
        </div>
        <div className="form">
          <Field name="app_password">
            <FieldLabel>App password</FieldLabel>
            <Input
              readOnly
              value={shown?.password ?? ''}
              spellCheck={false}
              autoComplete="off"
              className="mail-app-password"
              onFocus={(event) => event.currentTarget.select()}
            />
          </Field>
          <div className="form-actions">
            <Button onClick={() => (shown ? copyText(shown.password) : undefined)}>Copy password</Button>
            <DialogClose data-variant="solid">I’ve entered it</DialogClose>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
