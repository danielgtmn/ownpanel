import { Button } from '@ui-sdk/react/button'
import { Checkbox, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { useState, type FormEvent, type ReactNode } from 'react'
import { FormDialog } from '../shared/ui/index.ts'
import type { FilterField, FilterOp, FilterRule } from './api.ts'
import {
  addCondition,
  CONDITIONS_MAX,
  FIELD_LABELS,
  FIELDS,
  folderLabel,
  hasRuleErrors,
  moveTargets,
  newRuleDraft,
  OP_LABELS,
  OPS,
  RULE_NAME_MAX,
  ruleDraft,
  ruleErrors,
  ruleFromDraft,
  VALUE_MAX,
  type ConditionDraft,
  type RuleDraft,
  type RuleErrors,
} from './filters.ts'

function CheckChoice({
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
    <Field className="account-choice" disabled={disabled}>
      <Checkbox checked={checked} onCheckedChange={(next) => onCheckedChange(next === true)}>
        <CheckboxIndicator />
      </Checkbox>
      <div className="choice-text">
        <FieldLabel>{label}</FieldLabel>
        {description ? <FieldDescription>{description}</FieldDescription> : null}
      </div>
    </Field>
  )
}

function ConditionRow({
  condition,
  number,
  error,
  removable,
  onChange,
  onRemove,
}: {
  condition: ConditionDraft
  number: number
  error?: string
  removable: boolean
  onChange: (change: Partial<ConditionDraft>) => void
  onRemove: () => void
}) {
  return (
    <li className="filter-condition">
      <Field className="filter-condition-field">
        <FieldLabel className="visually-hidden">Part of the mail, condition {number}</FieldLabel>
        <Select value={condition.field} onValueChange={(value) => value && onChange({ field: value as FilterField })}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {FIELDS.map((field) => (
              <SelectItem key={field} value={field}>
                {FIELD_LABELS[field]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field className="filter-condition-op">
        <FieldLabel className="visually-hidden">How it compares, condition {number}</FieldLabel>
        <Select value={condition.op} onValueChange={(value) => value && onChange({ op: value as FilterOp })}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {OPS.map((op) => (
              <SelectItem key={op} value={op}>
                {OP_LABELS[op]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field className="filter-condition-value" invalid={error ? true : undefined}>
        <FieldLabel className="visually-hidden">Text to look for, condition {number}</FieldLabel>
        <Input
          value={condition.value}
          maxLength={VALUE_MAX}
          autoComplete="off"
          placeholder={condition.field === 'subject' ? 'Invoice' : 'news@example.com'}
          onValueChange={(value) => onChange({ value })}
        />
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      {removable ? (
        <Button className="filter-condition-remove" onClick={onRemove}>
          Remove<span className="visually-hidden"> condition {number}</span>
        </Button>
      ) : null}
    </li>
  )
}

function RuleForm({
  rule,
  folders,
  mayForward,
  onSubmit,
}: {
  rule: FilterRule | null
  folders: string[]
  mayForward: boolean
  onSubmit: (rule: FilterRule) => void
}) {
  const [draft, setDraft] = useState<RuleDraft>(() => (rule ? ruleDraft(rule, folders) : newRuleDraft(folders)))
  const [errors, setErrors] = useState<RuleErrors>({})
  const targets = moveTargets(folders, rule?.folder)

  function change(next: Partial<RuleDraft>) {
    setDraft((current) => ({ ...current, ...next }))
    setErrors({})
  }

  function changeCondition(key: string, next: Partial<ConditionDraft>) {
    change({
      conditions: draft.conditions.map((condition) => (condition.key === key ? { ...condition, ...next } : condition)),
    })
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = ruleErrors(draft)
    if (hasRuleErrors(found)) {
      setErrors(found)
      return
    }
    onSubmit(ruleFromDraft(draft))
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Name</FieldLabel>
        <Input
          value={draft.name}
          maxLength={RULE_NAME_MAX}
          placeholder="Newsletters"
          autoComplete="off"
          onValueChange={(name) => change({ name })}
        />
        <FieldDescription>Optional, to tell your rules apart.</FieldDescription>
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>

      <fieldset className="filter-group">
        <legend>When mail fits</legend>
        {draft.conditions.length > 1 ? (
          <RadioGroup
            aria-label="Which conditions must fit"
            value={draft.matchAny ? 'any' : 'all'}
            onValueChange={(value) => value && change({ matchAny: value === 'any' })}
          >
            <Field>
              <Radio value="all">
                <RadioIndicator />
              </Radio>
              <FieldLabel>All conditions</FieldLabel>
            </Field>
            <Field>
              <Radio value="any">
                <RadioIndicator />
              </Radio>
              <FieldLabel>Any condition</FieldLabel>
            </Field>
          </RadioGroup>
        ) : null}
        <ul className="filter-conditions">
          {draft.conditions.map((condition, index) => (
            <ConditionRow
              key={condition.key}
              condition={condition}
              number={index + 1}
              error={errors.conditions?.[condition.key]}
              removable={draft.conditions.length > 1}
              onChange={(next) => changeCondition(condition.key, next)}
              onRemove={() => change({ conditions: draft.conditions.filter((other) => other.key !== condition.key) })}
            />
          ))}
        </ul>
        {draft.conditions.length < CONDITIONS_MAX ? (
          <div>
            <Button onClick={() => change({ conditions: addCondition(draft.conditions) })}>Add condition</Button>
          </div>
        ) : null}
      </fieldset>

      <fieldset className="filter-group">
        <legend>Then</legend>
        <CheckChoice
          label="Move to a folder"
          description={targets.length === 0 ? 'There are no folders yet. Create one in your mail program first.' : undefined}
          checked={draft.move}
          disabled={targets.length === 0}
          onCheckedChange={(move) => change(move ? { move, delete: false } : { move })}
        />
        {draft.move ? (
          <Field className="filter-indent" invalid={errors.folder ? true : undefined}>
            <FieldLabel>Folder</FieldLabel>
            <Select value={draft.folder || null} onValueChange={(value) => value && change({ folder: value })}>
              <SelectTrigger>
                <SelectValue placeholder="Choose a folder" />
              </SelectTrigger>
              <SelectContent>
                {targets.map((folder) => (
                  <SelectItem key={folder} value={folder}>
                    {folderLabel(folder)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {errors.folder ? <FieldError match>{errors.folder}</FieldError> : null}
          </Field>
        ) : null}
        <CheckChoice label="Mark as read" checked={draft.markRead} onCheckedChange={(markRead) => change({ markRead })} />
        <CheckChoice
          label="Forward a copy"
          description={mayForward ? undefined : 'Forwarding is turned off for this mailbox by the people who manage it.'}
          checked={draft.forward}
          disabled={!mayForward && !draft.forward}
          onCheckedChange={(forward) => change({ forward })}
        />
        {draft.forward ? (
          <Field className="filter-indent" invalid={errors.forwardTo ? true : undefined}>
            <FieldLabel>Forward to</FieldLabel>
            <Input
              type="email"
              value={draft.forwardTo}
              placeholder="anna@example.com"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              onValueChange={(forwardTo) => change({ forwardTo })}
            />
            {errors.forwardTo ? <FieldError match>{errors.forwardTo}</FieldError> : null}
          </Field>
        ) : null}
        <CheckChoice
          label="Delete"
          description="The mail is deleted at once and never reaches a folder, not even the trash."
          checked={draft.delete}
          onCheckedChange={(remove) => change(remove ? { delete: true, move: false } : { delete: false })}
        />
      </fieldset>
      {errors.actions ? (
        <p className="form-error" role="alert">
          {errors.actions}
        </p>
      ) : null}
      <p className="muted account-dialog-note">The rule takes effect once you save your filters.</p>
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid">
          {rule ? 'Change rule' : 'Add rule'}
        </Button>
      </div>
    </form>
  )
}

export function FilterRuleDialog({
  open,
  rule,
  folders,
  mayForward,
  onOpenChange,
  onSubmit,
}: {
  open: boolean
  rule: FilterRule | null
  folders: string[]
  mayForward: boolean
  onOpenChange: (open: boolean) => void
  onSubmit: (rule: FilterRule) => void
}) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={rule ? 'Change rule' : 'Add rule'}
      description="What happens to new mail that fits. Only the first rule that fits is used."
    >
      <RuleForm rule={rule} folders={folders} mayForward={mayForward} onSubmit={onSubmit} />
    </FormDialog>
  )
}
