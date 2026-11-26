import { DateInput } from '@ui-sdk/react/date-input'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import {
  NumberInput,
  NumberInputControl,
  NumberInputDecrement,
  NumberInputGroup,
  NumberInputIncrement,
} from '@ui-sdk/react/number-input'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { Textarea } from '@ui-sdk/react/textarea'
import type { ReactNode } from 'react'
import {
  DAYS_MAX,
  DAYS_MIN,
  SUBJECT_MAX,
  type ForwardingDraft,
  type OutOfOfficeDraft,
  type OutOfOfficeErrors,
} from './rules.ts'
import './mailrules.css'

export function SwitchChoice({
  label,
  description,
  checked,
  disabled,
  onCheckedChange,
}: {
  label: string
  description?: ReactNode
  checked: boolean
  disabled?: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <Field className="mailrules-choice" disabled={disabled}>
      <Switch checked={checked} onCheckedChange={(next) => onCheckedChange(next === true)}>
        <SwitchThumb />
      </Switch>
      <div className="choice-text">
        <FieldLabel>{label}</FieldLabel>
        {description ? <FieldDescription>{description}</FieldDescription> : null}
      </div>
    </Field>
  )
}

// The fields of an out-of-office reply. The switch turns it on; the rest stays editable either way, so a reply
// can be written before it's turned on.
export function OutOfOfficeFields({
  draft,
  errors,
  switchLabel,
  switchDescription,
  onChange,
}: {
  draft: OutOfOfficeDraft
  errors: OutOfOfficeErrors
  switchLabel: string
  switchDescription?: ReactNode
  onChange: (change: Partial<OutOfOfficeDraft>) => void
}) {
  return (
    <div className="mailrules-fields">
      <SwitchChoice
        label={switchLabel}
        description={switchDescription}
        checked={draft.enabled}
        onCheckedChange={(enabled) => onChange({ enabled })}
      />
      <Field name="subject" invalid={errors.subject ? true : undefined}>
        <FieldLabel>Subject</FieldLabel>
        <Input
          value={draft.subject}
          maxLength={SUBJECT_MAX}
          placeholder="Out of office"
          autoComplete="off"
          onValueChange={(subject) => onChange({ subject })}
        />
        <FieldDescription>Leave it empty to answer with “Auto: ” and the subject of the mail.</FieldDescription>
        {errors.subject ? <FieldError match>{errors.subject}</FieldError> : null}
      </Field>
      <Field name="text" invalid={errors.text ? true : undefined}>
        <FieldLabel>Reply</FieldLabel>
        <Textarea
          value={draft.text}
          rows={5}
          placeholder="I'm away until Monday and will answer when I'm back."
          onValueChange={(text) => onChange({ text })}
        />
        {errors.text ? <FieldError match>{errors.text}</FieldError> : null}
      </Field>
      <div className="mailrules-dates">
        <Field name="start" invalid={errors.dates ? true : undefined}>
          <FieldLabel>First day</FieldLabel>
          <DateInput value={draft.start || null} onValueChange={(start) => onChange({ start: start ?? '' })} />
          <FieldDescription>Optional. Empty: from now on.</FieldDescription>
        </Field>
        <Field name="end" invalid={errors.dates ? true : undefined}>
          <FieldLabel>Last day</FieldLabel>
          <DateInput value={draft.end || null} onValueChange={(end) => onChange({ end: end ?? '' })} />
          <FieldDescription>Optional. Empty: until it's turned off.</FieldDescription>
          {errors.dates ? <FieldError match>{errors.dates}</FieldError> : null}
        </Field>
      </div>
      <Field name="days" invalid={errors.days ? true : undefined}>
        <FieldLabel>Reply to the same sender at most once every</FieldLabel>
        <div className="mailrules-days">
          <NumberInput
            value={draft.days}
            min={DAYS_MIN}
            max={DAYS_MAX}
            step={1}
            format={{ useGrouping: false, maximumFractionDigits: 0 }}
            onValueChange={(days) => onChange({ days })}
          >
            <NumberInputGroup>
              <NumberInputDecrement aria-label="Fewer days" />
              <NumberInputControl />
              <NumberInputIncrement aria-label="More days" />
            </NumberInputGroup>
          </NumberInput>
          <span>{draft.days === 1 ? 'day' : 'days'}</span>
        </div>
        <FieldDescription>
          Mailing lists, automatic messages and junk never get a reply.
        </FieldDescription>
        {errors.days ? <FieldError match>{errors.days}</FieldError> : null}
      </Field>
    </div>
  )
}

export function ForwardingFields({
  draft,
  error,
  switchLabel,
  onChange,
}: {
  draft: ForwardingDraft
  error?: string | null
  switchLabel: string
  onChange: (change: Partial<ForwardingDraft>) => void
}) {
  return (
    <div className="mailrules-fields">
      <SwitchChoice label={switchLabel} checked={draft.enabled} onCheckedChange={(enabled) => onChange({ enabled })} />
      <Field name="forward_to" invalid={error ? true : undefined}>
        <FieldLabel>Forward to</FieldLabel>
        <Input
          type="email"
          value={draft.to}
          placeholder="anna@example.com"
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          onValueChange={(to) => onChange({ to })}
        />
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      <SwitchChoice
        label="Keep a copy in this mailbox"
        description="Without it, forwarded mail is not kept here."
        checked={draft.keepCopy}
        onCheckedChange={(keepCopy) => onChange({ keepCopy })}
      />
    </div>
  )
}
