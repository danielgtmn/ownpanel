import { describe, expect, it } from 'vitest'
import type { User } from '../../shared/session/index.ts'
import { isSelf, otherRole, roleChangeQuestion, validateNewUser } from './rules.ts'

const anna: User = { id: 1, name: 'anna', role: 'admin', created_at: '2026-01-01T00:00:00Z' }
const max: User = { id: 2, name: 'max', role: 'member', created_at: '2026-01-02T00:00:00Z' }

describe('users rules', () => {
  it('flips the role', () => {
    expect(otherRole('admin')).toBe('member')
    expect(otherRole('member')).toBe('admin')
  })

  it('recognises the signed-in person', () => {
    expect(isSelf(anna, anna)).toBe(true)
    expect(isSelf(anna, max)).toBe(false)
  })

  it('validates a new person', () => {
    expect(validateNewUser('max', 'a-long-enough-password')).toEqual({})
    expect(validateNewUser('Max', 'short')).toEqual({
      name: expect.stringMatching(/lowercase/i),
      password: expect.stringMatching(/at least 10/i),
    })
  })

  it('words the role change question for each direction', () => {
    expect(roleChangeQuestion(max).confirmLabel).toBe('Make administrator')
    expect(roleChangeQuestion(anna).confirmLabel).toBe('Make member')
  })
})
