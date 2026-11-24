import { describe, expect, it } from 'vitest'
import { validatePassword, validatePasswordPair, validateUserName } from './validation.ts'

describe('validateUserName', () => {
  it.each(['abc', 'anna', 'max.mustermann', 'a_b-c', 'user42', 'a'.repeat(32)])('accepts %s', (name) => {
    expect(validateUserName(name)).toBeNull()
  })

  it('asks for a name when empty', () => {
    expect(validateUserName('  ')).toMatch(/enter a name/i)
  })

  it('rejects uppercase letters', () => {
    expect(validateUserName('Anna')).toMatch(/lowercase/i)
  })

  it('rejects spaces and other symbols', () => {
    expect(validateUserName('max muster')).toMatch(/only lowercase letters/i)
    expect(validateUserName('max@home')).toMatch(/only lowercase letters/i)
  })

  it('enforces 3 to 32 characters', () => {
    expect(validateUserName('ab')).toMatch(/at least 3/i)
    expect(validateUserName('a'.repeat(33))).toMatch(/at most 32/i)
  })
})

describe('validatePassword', () => {
  it('accepts 10 to 200 characters', () => {
    expect(validatePassword('x'.repeat(10))).toBeNull()
    expect(validatePassword('x'.repeat(200))).toBeNull()
  })

  it('rejects empty, short and long passwords', () => {
    expect(validatePassword('')).toMatch(/enter a password/i)
    expect(validatePassword('x'.repeat(9))).toMatch(/at least 10/i)
    expect(validatePassword('x'.repeat(201))).toMatch(/at most 200/i)
  })

  it('does not trim spaces away', () => {
    expect(validatePassword(' '.repeat(10))).toBeNull()
  })
})

describe('validatePasswordPair', () => {
  it('passes when both match and are long enough', () => {
    expect(validatePasswordPair('correct horse battery', 'correct horse battery')).toEqual({})
  })

  it('reports a mismatch on the repeat field', () => {
    const errors = validatePasswordPair('correct horse battery', 'correct horse batter')
    expect(errors.password).toBeUndefined()
    expect(errors.repeat).toMatch(/don't match/i)
  })

  it('asks to repeat when the repeat is empty', () => {
    expect(validatePasswordPair('correct horse battery', '').repeat).toMatch(/again/i)
  })

  it('reports a weak password and still checks the repeat', () => {
    const errors = validatePasswordPair('short', 'other')
    expect(errors.password).toMatch(/at least 10/i)
    expect(errors.repeat).toMatch(/don't match/i)
  })
})
