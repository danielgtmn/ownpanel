import { describe, expect, it } from 'vitest'
import type { User } from '../shared/session/index.ts'
import { screenFor } from './screen.ts'

const admin: User = { id: 1, name: 'anna', role: 'admin', created_at: '2026-01-01T00:00:00Z' }
const member: User = { id: 2, name: 'max', role: 'member', created_at: '2026-01-01T00:00:00Z' }

describe('screenFor', () => {
  it('shows the wizard to an administrator until the setup is finished', () => {
    expect(screenFor({ status: 'signedIn', user: admin, setupComplete: false })).toBe('wizard')
    expect(screenFor({ status: 'signedIn', user: admin, setupComplete: true })).toBe('panel')
  })

  it('never shows the wizard to members', () => {
    expect(screenFor({ status: 'signedIn', user: member, setupComplete: false })).toBe('panel')
  })

  it('keeps the other screens', () => {
    expect(screenFor({ status: 'setup' })).toBe('setup')
    expect(screenFor({ status: 'loading' })).toBe('loading')
    expect(screenFor({ status: 'signedOut', notice: null })).toBe('signedOut')
    expect(screenFor({ status: 'unreachable', message: 'x' })).toBe('unreachable')
  })
})
