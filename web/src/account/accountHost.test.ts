import { describe, expect, it } from 'vitest'
import { matchAccountHost } from './accountHost.ts'

describe('matchAccountHost', () => {
  it('is the mail server name when the page was opened under it', () => {
    expect(matchAccountHost('mail.example.com', 'mail.example.com')).toBe('mail.example.com')
    expect(matchAccountHost('mail.example.com', 'MAIL.example.com.')).toBe('mail.example.com')
  })

  it('is null for the panel', () => {
    expect(matchAccountHost(null, 'panel.example.com')).toBeNull()
    expect(matchAccountHost('', 'panel.example.com')).toBeNull()
    expect(matchAccountHost('mail.example.com', 'panel.example.com')).toBeNull()
  })
})
