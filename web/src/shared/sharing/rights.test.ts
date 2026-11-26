import { describe, expect, it } from 'vitest'
import {
  addressList,
  normaliseRights,
  rightsText,
  toggleRight,
  validatePersonAddress,
  validateRights,
  withGrant,
  withoutGrant,
} from './rights.ts'

describe('rights', () => {
  it('keeps known rights once, in order, and adds Read to Write', () => {
    expect(normaliseRights(['send-as', 'write', 'bogus', 'send-as'])).toEqual(['read', 'write', 'send-as'])
    expect(normaliseRights([])).toEqual([])
  })

  it('ticks Read with Write and takes Write away with Read', () => {
    expect(toggleRight([], 'write', true)).toEqual(['read', 'write'])
    expect(toggleRight(['read', 'write', 'manage'], 'read', false)).toEqual(['manage'])
    expect(toggleRight(['read', 'write'], 'write', false)).toEqual(['read'])
    expect(toggleRight(['read'], 'send-on-behalf', true)).toEqual(['read', 'send-on-behalf'])
  })

  it('names rights for people without repeating Read', () => {
    expect(rightsText(['read'])).toBe('Read')
    expect(rightsText(['read', 'write', 'send-as'])).toBe('Read and write, Send as')
    expect(rightsText(['manage', 'send-on-behalf'])).toBe('Manage filters and out-of-office, Send on behalf')
    expect(rightsText([])).toBe('No rights')
    expect(rightsText(['read', 'write', 'manage', 'send-as', 'send-on-behalf']).toLowerCase()).not.toMatch(
      /imap|acl|smtp|sieve|grant/,
    )
  })

  it('asks for at least one right and a whole address', () => {
    expect(validateRights([])).toMatch(/at least one/)
    expect(validateRights(['read'])).toBeNull()
    expect(validatePersonAddress('')).toMatch(/Enter the address/)
    expect(validatePersonAddress('anna')).toMatch(/whole e-mail address/)
    expect(validatePersonAddress(' anna@example.com ')).toBeNull()
  })
})

describe('lists of people', () => {
  const list = [
    { address: 'anna@example.com', display_name: 'Anna', rights: ['read'] },
    { address: 'ben@example.com', display_name: 'Ben', rights: ['write'] },
  ]

  it('adds a person or changes their rights, comparing addresses without case', () => {
    expect(withGrant(list, 'Anna@Example.com', ['read', 'send-as'])).toEqual([
      { address: 'anna@example.com', rights: ['read', 'send-as'] },
      { address: 'ben@example.com', rights: ['read', 'write'] },
    ])
    expect(withGrant(list, 'carl@example.com', ['write'])).toHaveLength(3)
    expect(withGrant([], ' Carl@example.com ', ['read'])).toEqual([{ address: 'carl@example.com', rights: ['read'] }])
  })

  it('takes a person out', () => {
    expect(withoutGrant(list, 'BEN@example.com')).toEqual([{ address: 'anna@example.com', rights: ['read'] }])
    expect(withoutGrant(list, 'nobody@example.com')).toHaveLength(2)
  })

  it('names a few addresses and counts the rest', () => {
    expect(addressList([])).toBe('')
    expect(addressList(['a@x.com'])).toBe('a@x.com')
    expect(addressList(['a@x.com', 'b@x.com'])).toBe('a@x.com and b@x.com')
    expect(addressList(['a@x.com', 'b@x.com', 'c@x.com', 'd@x.com'])).toBe('a@x.com, b@x.com and 2 more')
  })
})
