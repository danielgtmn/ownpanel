import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { hrefFor, newMailHref } from '../../shared/routing/index.ts'
import { ErrorState, FormDialog, LoadingState, StatusBadge } from '../../shared/ui/index.ts'
import { applyTemplate, listTemplates } from './api.ts'
import { fullName } from './records.ts'
import { actionLabel, actionTone, cleanParams, missingParams, summarize, summaryText, valueDiff, type ParamErrors } from './templates.ts'
import type { RecordTemplate, TemplateChange, TemplateResult } from './types.ts'

function ChangeItem({ change, zone }: { change: TemplateChange; zone: string }) {
  const { removed, added, kept } = valueDiff(change)
  return (
    <li className="template-change">
      <div className="template-change-head">
        <strong className="record-name">
          {fullName(change.name, zone)} <span className="record-type">{change.type}</span>
        </strong>
        <StatusBadge tone={actionTone(change.action)}>{actionLabel(change.action)}</StatusBadge>
      </div>
      {change.note ? <p className="muted">{change.note}</p> : null}
      {change.action === 'ok' || change.action === 'conflict' ? (
        change.current.length > 0 ? (
          <ul className="record-values" aria-label="Values now">
            {change.current.map((value) => (
              <li key={value}>{value}</li>
            ))}
          </ul>
        ) : null
      ) : (
        <ul className="record-values template-diff" aria-label="Values afterwards">
          {removed.map((value) => (
            <li key={`-${value}`} data-diff="removed">
              <span className="visually-hidden">Removed: </span>
              {value}
            </li>
          ))}
          {added.map((value) => (
            <li key={`+${value}`} data-diff="added">
              <span className="visually-hidden">Added: </span>
              {value}
            </li>
          ))}
          {kept.map((value) => (
            <li key={`=${value}`} data-diff="kept">
              <span className="visually-hidden">Stays: </span>
              {value}
            </li>
          ))}
        </ul>
      )}
    </li>
  )
}

function Preview({
  zone,
  result,
  onBack,
  onApply,
}: {
  zone: string
  result: TemplateResult
  onBack: () => void
  onApply: () => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const summary = summarize(result.changes)
  const canApply = summary.writes > 0 && summary.conflicts === 0

  async function apply() {
    setBusy(true)
    setError(null)
    try {
      await onApply()
    } catch (failure) {
      setError(errorMessage(failure))
      setBusy(false)
    }
  }

  return (
    <div className="form">
      <p className="dialog-text" role="status">
        {summaryText(summary)}
      </p>
      <ul className="template-changes" aria-label="What changes">
        {result.changes.map((change) => (
          <ChangeItem key={`${change.type}:${change.name}`} change={change} zone={zone} />
        ))}
      </ul>
      {error ? (
        <p className="form-error" role="alert">
          {error}
        </p>
      ) : null}
      <div className="form-actions">
        <Button disabled={busy} onClick={onBack}>
          Back
        </Button>
        {canApply ? (
          <Button data-variant="solid" disabled={busy} onClick={apply}>
            {busy ? 'Changing…' : 'Change the records'}
          </Button>
        ) : (
          <DialogClose>Close</DialogClose>
        )}
      </div>
    </div>
  )
}

function OwnMail({ zone }: { zone: string }) {
  return (
    <div className="form">
      <p className="dialog-text">
        The records for e-mail on this server depend on the domain's e-mail settings. Once e-mail is turned on for{' '}
        {zone}, its e-mail page sets them up for you and shows what changes.
      </p>
      <p className="dialog-text template-links">
        <a href={hrefFor('mail', zone)}>Open the e-mail page of {zone}</a>
        <a href={newMailHref(zone)}>Turn on e-mail for {zone}</a>
      </p>
    </div>
  )
}

function Choose({
  templates,
  zone,
  initial,
  onPreview,
}: {
  templates: RecordTemplate[]
  zone: string
  initial: { id: string; values: Record<string, string> }
  onPreview: (template: RecordTemplate, values: Record<string, string>) => Promise<void>
}) {
  const [id, setId] = useState(initial.id || templates[0]?.id || '')
  const [values, setValues] = useState<Record<string, string>>(initial.values)
  const [errors, setErrors] = useState<ParamErrors & { form?: string }>({})
  const [busy, setBusy] = useState(false)
  const template = templates.find((item) => item.id === id)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!template || template.own_mail) return
    const found = missingParams(template, values)
    if (Object.keys(found).length > 0) {
      setErrors(found)
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onPreview(template, values)
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="template">
        <FieldLabel>What to set up</FieldLabel>
        <Select
          value={id}
          onValueChange={(value) => {
            if (!value) return
            setId(value)
            setErrors({})
          }}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {templates.map((item) => (
              <SelectItem key={item.id} value={item.id}>
                {item.title}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {template ? <FieldDescription>{template.description}</FieldDescription> : null}
      </Field>

      {template?.own_mail ? <OwnMail zone={zone} /> : null}

      {template && !template.own_mail
        ? template.params.map((param, index) => (
            <Field key={`${template.id}:${param.id}`} name={param.id} invalid={errors[param.id] ? true : undefined}>
              <FieldLabel>
                {param.label}
                {param.optional ? <span className="muted"> (optional)</span> : null}
              </FieldLabel>
              <Input
                value={values[param.id] ?? ''}
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                placeholder={param.placeholder}
                autoFocus={index === 0}
                onValueChange={(value) => {
                  setValues((current) => ({ ...current, [param.id]: value }))
                  setErrors((current) => ({ ...current, [param.id]: undefined, form: undefined }) as ParamErrors)
                }}
              />
              {param.description ? <FieldDescription>{param.description}</FieldDescription> : null}
              {errors[param.id] ? <FieldError match>{errors[param.id]}</FieldError> : null}
            </Field>
          ))
        : null}

      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>{template?.own_mail ? 'Close' : 'Cancel'}</DialogClose>
        {template && !template.own_mail ? (
          <Button type="submit" data-variant="solid" disabled={busy}>
            {busy ? 'Checking…' : 'Show what changes'}
          </Button>
        ) : null}
      </div>
    </form>
  )
}

function TemplateFlow({
  connectionId,
  zone,
  onApplied,
}: {
  connectionId: number
  zone: string
  onApplied: (title: string, result: TemplateResult) => Promise<void>
}) {
  const templates = useResource(listTemplates, 'dns-templates')
  const [chosen, setChosen] = useState<{ id: string; values: Record<string, string> }>({ id: '', values: {} })
  const [preview, setPreview] = useState<{ template: RecordTemplate; params: Record<string, string>; result: TemplateResult } | null>(null)

  if (templates.status === 'loading') return <LoadingState label="Loading…" />
  if (templates.status === 'error') {
    return <ErrorState title="Couldn't load the choices" message={errorMessage(templates.error)} onRetry={templates.retry} />
  }

  if (preview) {
    return (
      <Preview
        zone={zone}
        result={preview.result}
        onBack={() => setPreview(null)}
        onApply={async () => {
          const result = await applyTemplate(connectionId, zone, preview.template.id, preview.params, true)
          await onApplied(preview.template.title, result)
        }}
      />
    )
  }

  return (
    <Choose
      templates={templates.data}
      zone={zone}
      initial={chosen}
      onPreview={async (template, values) => {
        const params = cleanParams(template, values)
        const result = await applyTemplate(connectionId, zone, template.id, params, false)
        setChosen({ id: template.id, values })
        setPreview({ template, params, result })
      }}
    />
  )
}

type TemplateDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  connectionId: number
  zone: string
  onApplied: (title: string, result: TemplateResult) => Promise<void>
}

export function TemplateDialog({ open, onOpenChange, connectionId, zone, onApplied }: TemplateDialogProps) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Set up a service"
      description={`Add the records a service needs to ${zone}. You see what changes before anything is changed.`}
    >
      <TemplateFlow connectionId={connectionId} zone={zone} onApplied={onApplied} />
    </FormDialog>
  )
}
