import { describe, expect, it } from 'vitest'
import { hasContent, newestFirst, parseReleaseDate } from './releases.ts'

describe('parseReleaseDate', () => {
  it('reads the UTC timestamp of a release id', () => {
    const date = parseReleaseDate('20261124T191433.015Z-1020de')
    expect(date?.toISOString()).toBe('2026-11-24T19:14:33.015Z')
  })

  it('accepts ids without milliseconds or suffix', () => {
    expect(parseReleaseDate('20260102T030405Z')?.toISOString()).toBe('2026-01-02T03:04:05.000Z')
  })

  it('pads short fractions', () => {
    expect(parseReleaseDate('20260102T030405.5Z-ab')?.getUTCMilliseconds()).toBe(500)
  })

  it('returns null for anything else', () => {
    expect(parseReleaseDate('')).toBeNull()
    expect(parseReleaseDate('latest')).toBeNull()
    expect(parseReleaseDate('2026-11-24T19:14:33Z')).toBeNull()
  })
})

describe('release helpers', () => {
  it('lists newest first without changing the input', () => {
    const releases = ['a', 'b', 'c']
    expect(newestFirst(releases)).toEqual(['c', 'b', 'a'])
    expect(releases).toEqual(['a', 'b', 'c'])
  })

  it('treats an empty current release as no content', () => {
    expect(hasContent({ name: 'x', owner: 'anna', domains: [], current_release: '', releases: [] })).toBe(false)
    expect(hasContent({ name: 'x', owner: 'anna', domains: [], current_release: 'r', releases: ['r'] })).toBe(true)
  })
})
