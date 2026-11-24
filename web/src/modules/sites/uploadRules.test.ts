import { describe, expect, it } from 'vitest'
import { rejectionMessage } from './uploadRules.ts'

const file = (name: string) => new File(['x'], name)

describe('rejectionMessage', () => {
  it('explains a wrong file type', () => {
    expect(rejectionMessage([{ file: file('a.rar'), reasons: ['file-type'] }])).toMatch(/\.zip, \.tar\.gz or \.tgz/)
  })

  it('explains a file that is too large', () => {
    expect(rejectionMessage([{ file: file('a.zip'), reasons: ['file-too-large'] }])).toMatch(/too large.*512/)
  })

  it('asks for a single file when several arrive', () => {
    const rejected = [
      { file: file('a.zip'), reasons: ['too-many-files'] },
      { file: file('b.zip'), reasons: ['too-many-files'] },
    ]
    expect(rejectionMessage(rejected)).toBe('Choose just one file.')
  })

  it('is empty when nothing was rejected', () => {
    expect(rejectionMessage([])).toBe('')
  })
})
