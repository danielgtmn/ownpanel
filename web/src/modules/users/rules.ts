import { validatePassword, validateUserName, type Role, type User } from '../../shared/session/index.ts'

export function otherRole(role: Role): Role {
  return role === 'admin' ? 'member' : 'admin'
}

export function isSelf(current: User, target: User): boolean {
  return current.id === target.id
}

export type NewUserErrors = { name?: string; password?: string }

export function validateNewUser(name: string, password: string): NewUserErrors {
  const errors: NewUserErrors = {}
  const nameError = validateUserName(name)
  if (nameError) errors.name = nameError
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
