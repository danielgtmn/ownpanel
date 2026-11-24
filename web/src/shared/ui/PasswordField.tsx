import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { PasswordInput, PasswordInputControl, PasswordInputToggle } from '@ui-sdk/react/password-input'

type PasswordFieldProps = {
  name: string
  label: string
  value: string
  onValueChange: (value: string) => void
  autoComplete: 'current-password' | 'new-password'
  description?: string
  error?: string | null
  autoFocus?: boolean
}

export function PasswordField({
  name,
  label,
  value,
  onValueChange,
  autoComplete,
  description,
  error,
  autoFocus,
}: PasswordFieldProps) {
  return (
    <Field name={name} invalid={error ? true : undefined}>
      <FieldLabel>{label}</FieldLabel>
      <PasswordInput autoComplete={autoComplete} value={value} onValueChange={onValueChange}>
        <PasswordInputControl autoFocus={autoFocus} />
        <PasswordInputToggle aria-label={`Show ${label.toLowerCase()}`} />
      </PasswordInput>
      {description ? <FieldDescription>{description}</FieldDescription> : null}
      {error ? <FieldError match>{error}</FieldError> : null}
    </Field>
  )
}
