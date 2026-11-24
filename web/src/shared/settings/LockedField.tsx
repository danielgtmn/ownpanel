import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { LOCKED_NOTE } from './rules.ts'

export function LockedField({ label, value }: { label: string; value: string }) {
  return (
    <Field className="locked-field">
      <FieldLabel>{label}</FieldLabel>
      <Input value={value || 'Not set'} readOnly />
      <FieldDescription>{LOCKED_NOTE}.</FieldDescription>
    </Field>
  )
}
