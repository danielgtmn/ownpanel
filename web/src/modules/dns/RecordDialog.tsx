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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { Textarea } from '@ui-sdk/react/textarea'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import {
  fullName,
  hasErrors,
  normalizeName,
  parseValues,
  RECORD_TYPES,
  routeServerError,
  TTL_MAX,
  TTL_MIN,
  typeHint,
  typePlaceholder,
  validateRecordDraft,
  valuesToText,
  type RecordDraftErrors,
} from './records.ts'
import type { RecordBody, RecordSet } from './types.ts'

export type RecordSubmit = (name: string, type: string, body: RecordBody) => Promise<void>

type FormErrors = RecordDraftErrors & { form?: string }

function RecordForm({
  zone,
  existing,
  onSubmit,
}: {
  zone: string
  existing: RecordSet | null
  onSubmit: RecordSubmit
}) {
  const editing = existing !== null
  const [name, setName] = useState(existing?.name ?? '')
  const [type, setType] = useState(existing?.type ?? 'A')
  const [valuesText, setValuesText] = useState(existing ? valuesToText(existing.values) : '')
  const [ttl, setTtl] = useState<number | null>(existing?.ttl ?? null)
  const [errors, setErrors] = useState<FormErrors>({})
  const [busy, setBusy] = useState(false)

  function clear(key: keyof FormErrors) {
    setErrors((current) => ({ ...current, [key]: undefined, form: undefined }))
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateRecordDraft({ name, type, valuesText, ttl }, zone)
    if (hasErrors(found)) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      const finalName = normalizeName(name)
      await onSubmit(finalName, type, { ttl, values: parseValues(valuesText) })
    } catch (error) {
      setErrors(routeServerError(errorMessage(error)))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Name</FieldLabel>
        <Input
          value={name}
          disabled={editing}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder="www"
          autoFocus={!editing}
          onValueChange={(value) => {
            setName(value)
            clear('name')
          }}
        />
        <FieldDescription>
          Use @ for the domain itself. Full address: <strong className="record-preview">{fullName(name, zone)}</strong>
        </FieldDescription>
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>

      <Field name="type" invalid={errors.type ? true : undefined}>
        <FieldLabel>Type</FieldLabel>
        <Select
          value={type}
          disabled={editing}
          onValueChange={(value) => {
            if (!value) return
            setType(value)
            clear('values')
            clear('type')
          }}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {RECORD_TYPES.map((item) => (
              <SelectItem key={item} value={item}>
                {item}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {errors.type ? <FieldError match>{errors.type}</FieldError> : null}
      </Field>

      <Field name="values" invalid={errors.values ? true : undefined}>
        <FieldLabel>Values</FieldLabel>
        <Textarea
          value={valuesText}
          rows={4}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder={typePlaceholder(type)}
          onValueChange={(value) => {
            setValuesText(value)
            clear('values')
          }}
        />
        <FieldDescription>One value per line. {typeHint(type)}</FieldDescription>
        {errors.values ? <FieldError match>{errors.values}</FieldError> : null}
      </Field>

      <Field invalid={errors.ttl ? true : undefined}>
        <FieldLabel>TTL (seconds)</FieldLabel>
        <NumberInput
          name="ttl"
          value={ttl}
          min={TTL_MIN}
          max={TTL_MAX}
          largeStep={60}
          format={{ useGrouping: false }}
          onValueChange={(value) => {
            setTtl(value)
            clear('ttl')
          }}
        >
          <NumberInputGroup>
            <NumberInputDecrement aria-label="Decrease TTL" />
            <NumberInputControl placeholder="Default" />
            <NumberInputIncrement aria-label="Increase TTL" />
          </NumberInputGroup>
        </NumberInput>
        <FieldDescription>
          How long others may remember this record, from {TTL_MIN} to {TTL_MAX}. Leave empty for the default.
        </FieldDescription>
        {errors.ttl ? <FieldError match>{errors.ttl}</FieldError> : null}
      </Field>

      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : editing ? 'Save record' : 'Add record'}
        </Button>
      </div>
    </form>
  )
}

type RecordDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  zone: string
  existing: RecordSet | null
  onSubmit: RecordSubmit
}

export function RecordDialog({ open, onOpenChange, zone, existing, onSubmit }: RecordDialogProps) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={existing ? 'Edit record' : 'Add record'}
      description={
        existing
          ? `Change what this ${existing.type} record points to. The name and type stay the same.`
          : `Add a DNS record to ${zone}.`
      }
    >
      <RecordForm zone={zone} existing={existing} onSubmit={onSubmit} />
    </FormDialog>
  )
}
