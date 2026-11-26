import { Checkbox, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { RIGHT_HINT, RIGHT_LABEL, RIGHTS, toggleRight, type Right } from './rights.ts'

// The rights as checkboxes. Write includes Read: while Write is ticked, Read
// stays ticked and can't be unticked.
export function RightsChoice({
  value,
  error,
  disabled,
  legend = 'What the person may do',
  onChange,
}: {
  value: readonly string[]
  error?: string
  disabled?: boolean
  legend?: string
  onChange: (rights: Right[]) => void
}) {
  const writing = value.includes('write')
  return (
    <fieldset className="rights-choice" aria-invalid={error ? true : undefined}>
      <legend>{legend}</legend>
      {RIGHTS.map((right) => {
        const locked = right === 'read' && writing
        return (
          <Field key={right} className="rights-choice-item" disabled={disabled || locked}>
            <Checkbox
              checked={value.includes(right) || locked}
              disabled={disabled || locked}
              onCheckedChange={(next) => onChange(toggleRight(value, right, next === true))}
            >
              <CheckboxIndicator />
            </Checkbox>
            <div className="choice-text">
              <FieldLabel>{RIGHT_LABEL[right]}</FieldLabel>
              <FieldDescription>{RIGHT_HINT[right]}</FieldDescription>
            </div>
          </Field>
        )
      })}
      {error ? (
        <p className="form-error" role="alert">
          {error}
        </p>
      ) : null}
    </fieldset>
  )
}
