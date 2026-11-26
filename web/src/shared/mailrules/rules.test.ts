import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  forwardingBody,
  forwardingDraft,
  forwardingError,
  hasErrors,
  outOfOfficeBody,
  outOfOfficeDraft,
  outOfOfficeErrors,
  outOfOfficeWhen,
  setByText,
  zoneOffset,
  type OutOfOfficeDraft,
} from './rules.ts'

const draft: OutOfOfficeDraft = {
  enabled: true,
  subject: '  Away  ',
  text: 'Back on Monday.\r\nAnna',
  start: '2026-12-01',
  end: '2026-12-24',
  days: 7,
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('zone offset', () => {
  it('writes the browser’s offset on the first day as +hhmm or -hhmm', () => {
    vi.spyOn(Date.prototype, 'getTimezoneOffset').mockReturnValue(-60)
    expect(zoneOffset('2026-12-01')).toBe('+0100')
    vi.spyOn(Date.prototype, 'getTimezoneOffset').mockReturnValue(330)
    expect(zoneOffset('2026-12-01')).toBe('-0530')
    vi.spyOn(Date.prototype, 'getTimezoneOffset').mockReturnValue(0)
    expect(zoneOffset('')).toBe('+0000')
  })

  it('takes the offset of the first day, not of today', () => {
    const spy = vi.spyOn(Date.prototype, 'getTimezoneOffset').mockImplementation(function (this: Date) {
      return this.getMonth() >= 3 && this.getMonth() <= 9 ? -120 : -60
    })
    expect(zoneOffset('2026-07-01', new Date(2026, 0, 15))).toBe('+0200')
    expect(zoneOffset('', new Date(2026, 0, 15))).toBe('+0100')
    spy.mockRestore()
  })
})

describe('out-of-office', () => {
  it('starts from the server’s answer with a default period', () => {
    expect(outOfOfficeDraft({ enabled: false, subject: '', text: '', days: 0 })).toEqual({
      enabled: false,
      subject: '',
      text: '',
      start: '',
      end: '',
      days: 7,
    })
    expect(outOfOfficeDraft({ enabled: true, start: '2026-12-01', days: 3 }).days).toBe(3)
  })

  it('sends exactly the fields the server knows, trimmed, with the zone', () => {
    vi.spyOn(Date.prototype, 'getTimezoneOffset').mockReturnValue(-60)
    expect(outOfOfficeBody(draft)).toEqual({
      enabled: true,
      subject: 'Away',
      text: 'Back on Monday.\nAnna',
      start: '2026-12-01',
      end: '2026-12-24',
      zone: '+0100',
      days: 7,
    })
  })

  it('checks like the server', () => {
    expect(hasErrors(outOfOfficeErrors(draft))).toBe(false)
    expect(outOfOfficeErrors({ ...draft, text: '  ' }).text).toBeTruthy()
    expect(outOfOfficeErrors({ ...draft, enabled: false, text: '' }).text).toBeUndefined()
    expect(outOfOfficeErrors({ ...draft, text: 'ä'.repeat(4097) }).text).toMatch(/too long/)
    expect(outOfOfficeErrors({ ...draft, subject: 'x'.repeat(201) }).subject).toBeTruthy()
    expect(outOfOfficeErrors({ ...draft, start: '2026-12-24', end: '2026-12-01' }).dates).toBeTruthy()
    expect(outOfOfficeErrors({ ...draft, days: 0 }).days).toBeTruthy()
    expect(outOfOfficeErrors({ ...draft, days: 91 }).days).toBeTruthy()
    expect(outOfOfficeErrors({ ...draft, days: null }).days).toBeTruthy()
  })

  it('says when the reply answers', () => {
    const day = (value: string) => value
    expect(outOfOfficeWhen({ start: '', end: '' }, day)).toBe('Until it is turned off')
    expect(outOfOfficeWhen({ start: 'a', end: 'b' }, day)).toBe('From a to b')
    expect(outOfOfficeWhen({ start: '', end: 'b' }, day)).toBe('Until b')
  })
})

describe('forwarding', () => {
  it('needs a whole address when it is on', () => {
    expect(forwardingError({ enabled: true, to: '', keepCopy: false })).toBeTruthy()
    expect(forwardingError({ enabled: true, to: 'anna', keepCopy: false })).toBeTruthy()
    expect(forwardingError({ enabled: true, to: 'anna@example.com', keepCopy: false })).toBeNull()
    expect(forwardingError({ enabled: false, to: '', keepCopy: false })).toBeNull()
  })

  it('sends the address in lower case', () => {
    expect(forwardingBody(forwardingDraft({ enabled: true, to: ' Anna@Example.com ', keep_copy: true }))).toEqual({
      enabled: true,
      to: 'anna@example.com',
      keep_copy: true,
    })
  })
})

describe('set by', () => {
  it('names who set it for the person, and nothing when the person did', () => {
    expect(setByText('', null, (value) => value)).toBeNull()
    expect(setByText('admin', '2026-11-25T10:00:00Z', () => '25 Nov')).toBe('Set by admin on 25 Nov')
    expect(setByText('admin', null, () => '')).toBe('Set by admin')
  })
})
