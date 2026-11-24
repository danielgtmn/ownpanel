export const USER_NAME_MIN = 3
export const USER_NAME_MAX = 32
export const PASSWORD_MIN = 10
export const PASSWORD_MAX = 200

export function validateUserName(value: string): string | null {
  const name = value.trim()
  if (!name) return 'Enter a name.'
  if (/[A-Z]/.test(name)) return 'Use lowercase letters only.'
  if (!/^[a-z0-9._-]+$/.test(name)) {
    return 'Use only lowercase letters, numbers, dots, underscores and hyphens, without spaces.'
  }
  if (name.length < USER_NAME_MIN) return `Use at least ${USER_NAME_MIN} characters.`
  if (name.length > USER_NAME_MAX) return `Use at most ${USER_NAME_MAX} characters.`
  return null
}

export function validatePassword(value: string): string | null {
  if (!value) return 'Enter a password.'
  const length = [...value].length
  if (length < PASSWORD_MIN) return `Use at least ${PASSWORD_MIN} characters.`
  if (length > PASSWORD_MAX) return `Use at most ${PASSWORD_MAX} characters.`
  return null
}

export type PasswordPairErrors = { password?: string; repeat?: string }

export function validatePasswordPair(password: string, repeat: string): PasswordPairErrors {
  const errors: PasswordPairErrors = {}
  const passwordError = validatePassword(password)
  if (passwordError) errors.password = passwordError
  if (!repeat) errors.repeat = 'Enter the password again.'
  else if (repeat !== password) errors.repeat = "The two passwords don't match."
  return errors
}
