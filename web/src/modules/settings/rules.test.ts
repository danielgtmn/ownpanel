import { describe, expect, it } from 'vitest'
import { ipAccessNotice } from './rules.ts'

describe('ipAccessNotice', () => {
  it('explains IP access only after the setup is finished without a domain', () => {
    expect(ipAccessNotice({ ip_access: true, setup_complete: true })).toMatch(
      /reachable by IP address because no panel domain is set/,
    )
    expect(ipAccessNotice({ ip_access: true, setup_complete: false })).toBeNull()
    expect(ipAccessNotice({ ip_access: false, setup_complete: true })).toBeNull()
  })
})
