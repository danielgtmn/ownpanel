import { validateEmail, validatePassword, validateUserName, type Role, type User } from '../../shared/session/index.ts'

export function otherRole(role: Role): Role {
  return role === 'admin' ? 'member' : 'admin'
}

export function isSelf(current: User, target: User): boolean {
  return current.id === target.id
}

export const EMAIL_HINT = 'Used to send a link when the person forgets their password. They can also sign in with it.'

export type NewUserErrors = { name?: string; email?: string; password?: string }

// The e-mail address is optional: empty is fine, anything else has to look like an address.
export function validateOptionalEmail(email: string): string | null {
  return email.trim() ? validateEmail(email) : null
}

export function validateNewUser(name: string, password: string, email = ''): NewUserErrors {
  const errors: NewUserErrors = {}
  const nameError = validateUserName(name)
  if (nameError) errors.name = nameError
  const emailError = validateOptionalEmail(email)
  if (emailError) errors.email = emailError
  const passwordError = validatePassword(password)
  if (passwordError) errors.password = passwordError
  return errors
}

export function roleChangeQuestion(user: User): { title: string; description: string; confirmLabel: string } {
  if (user.role === 'admin') {
    return {
      title: `Make ${user.name} a member?`,
      description: `${user.name} will only see their own sites and can no longer manage people.`,
      confirmLabel: 'Make member',
    }
  }
  return {
    title: `Make ${user.name} an administrator?`,
    description: `${user.name} will see every site and be able to manage people and read the activity.`,
    confirmLabel: 'Make administrator',
  }
}
