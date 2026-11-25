import { describe, expect, it } from 'vitest'
import type { PanelModule } from '../modules/types.ts'
import type { User } from '../shared/session/index.ts'
import { defaultModule, moduleFor, visibleModules } from './modules.ts'

const page = () => Promise.resolve(() => null)
const everyone: PanelModule = { id: 'sites', label: 'Sites', basePath: '/sites', page }
const adminsOnly: PanelModule = {
  id: 'users',
  label: 'People',
  basePath: '/people',
  page,
  visible: (user) => user.role === 'admin',
}
const all = [everyone, adminsOnly]

const admin: User = { id: 1, name: 'anna', role: 'admin', created_at: '2026-01-01T00:00:00Z' }
const member: User = { id: 2, name: 'max', role: 'member', created_at: '2026-01-01T00:00:00Z' }

describe('visibleModules', () => {
  it('keeps modules without a rule for everyone', () => {
    expect(visibleModules(all, member)).toEqual([everyone])
  })

  it('shows admin-only modules to administrators', () => {
    expect(visibleModules(all, admin)).toEqual(all)
  })
})

describe('moduleFor', () => {
  it('finds a module by its first path segment and passes the rest on', () => {
    expect(moduleFor(all, ['sites', 'blog'])).toEqual({ module: everyone, rest: ['blog'] })
  })

  it('treats a hidden module like an unknown address', () => {
    expect(moduleFor(visibleModules(all, member), ['people'])).toBeNull()
    expect(moduleFor(visibleModules(all, admin), ['people'])?.module).toBe(adminsOnly)
  })

  it('returns nothing for an empty address', () => {
    expect(moduleFor(all, [])).toBeNull()
  })
})

describe('defaultModule', () => {
  it('is the first visible module', () => {
    expect(defaultModule(visibleModules(all, member))).toBe(everyone)
    expect(defaultModule([])).toBeUndefined()
  })
})
