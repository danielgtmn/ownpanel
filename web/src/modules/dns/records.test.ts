import { describe, expect, it } from 'vitest'
import {
  fullName,
  isEditableRecordSet,
  isIPv4,
  isIPv6,
  parseValues,
  routeServerError,
  sortRecordSets,
  ttlLabel,
  validateName,
  validateRecordDraft,
  validateTtl,
  validateValues,
} from './records.ts'
import type { RecordSet } from './types.ts'

const set = (name: string, type: string): RecordSet => ({ name, type, ttl: null, values: ['x'], locked: false })

describe('sortRecordSets', () => {
  it('puts the domain itself first, then names, then types', () => {
    const sorted = sortRecordSets([
      set('www', 'CNAME'),
      set('_dmarc', 'TXT'),
      set('@', 'TXT'),
      set('mail', 'A'),
      set('@', 'A'),
      set('@', 'MX'),
      set('@', 'AAAA'),
    ])
    expect(sorted.map((item) => `${item.name}/${item.type}`)).toEqual([
      '@/A',
      '@/AAAA',
      '@/MX',
      '@/TXT',
      '_dmarc/TXT',
      'mail/A',
      'www/CNAME',
    ])
  })

  it('puts the SOA and the NS records of the domain itself last', () => {
    const sorted = sortRecordSets([
      set('@', 'NS'),
      set('www', 'CNAME'),
      set('@', 'SOA'),
      set('sub', 'NS'),
      set('@', 'A'),
    ])
    expect(sorted.map((item) => `${item.name}/${item.type}`)).toEqual([
      '@/A',
      'sub/NS',
      'www/CNAME',
      '@/NS',
      '@/SOA',
    ])
  })

  it('does not change the input', () => {
    const input = [set('b', 'A'), set('@', 'A')]
    sortRecordSets(input)
    expect(input[0].name).toBe('b')
  })
})

describe('isEditableRecordSet', () => {
  it('never allows the SOA', () => {
    expect(isEditableRecordSet(set('@', 'SOA'), false)).toBe(false)
  })

  it('never allows NS on the domain itself, but allows it on a subdomain', () => {
    expect(isEditableRecordSet(set('@', 'NS'), false)).toBe(false)
    expect(isEditableRecordSet(set('sub', 'NS'), false)).toBe(true)
  })

  it('allows ordinary records', () => {
    expect(isEditableRecordSet(set('@', 'A'), false)).toBe(true)
  })

  it('never allows locked records or records of a read only domain', () => {
    expect(isEditableRecordSet({ ...set('www', 'A'), locked: true }, false)).toBe(false)
    expect(isEditableRecordSet(set('www', 'A'), true)).toBe(false)
    expect(isEditableRecordSet(set('sub', 'NS'), true)).toBe(false)
  })

  it('does not accept an NS record on the domain itself from the dialog', () => {
    expect(
      validateRecordDraft({ name: '@', type: 'NS', valuesText: 'ns1.example.com', ttl: null }, 'example.com').values,
    ).toBeTruthy()
  })
})

describe('names and previews', () => {
  it('shows the domain itself for @ and for an empty name', () => {
    expect(fullName('@', 'example.com')).toBe('example.com')
    expect(fullName('', 'example.com')).toBe('example.com')
    expect(fullName('  ', 'example.com')).toBe('example.com')
  })

  it('adds the domain to relative names', () => {
    expect(fullName('www', 'example.com')).toBe('www.example.com')
    expect(fullName(' WWW ', 'example.com')).toBe('www.example.com')
    expect(fullName('*', 'example.com')).toBe('*.example.com')
    expect(fullName('_dmarc', 'example.com')).toBe('_dmarc.example.com')
  })

  it('validates names', () => {
    for (const ok of ['@', 'www', '*', '*.dev', '_dmarc', 'a.b.c', 'mail-1']) {
      expect(validateName(ok, 'example.com')).toBeNull()
    }
    expect(validateName('', 'example.com')).toMatch(/enter a name/i)
    expect(validateName('www.example.com', 'example.com')).toMatch(/without/)
    expect(validateName('a.*', 'example.com')).toMatch(/first part/)
    expect(validateName('bad name', 'example.com')).toMatch(/letters/)
    expect(validateName('-a', 'example.com')).toMatch(/letters/)
    expect(validateName('a.', 'example.com')).toMatch(/dot/)
  })

  it('shows a default ttl label', () => {
    expect(ttlLabel(null)).toBe('Default')
    expect(ttlLabel(300)).toBe('300 seconds')
  })
})

describe('addresses', () => {
  it('checks IPv4', () => {
    expect(isIPv4('203.0.113.10')).toBe(true)
    expect(isIPv4('256.0.0.1')).toBe(false)
    expect(isIPv4('1.2.3')).toBe(false)
    expect(isIPv4('01.2.3.4')).toBe(false)
    expect(isIPv4('1.2.3.4.5')).toBe(false)
  })

  it('checks IPv6', () => {
    for (const ok of ['2001:db8::1', '::1', '::', 'fe80::1:2', '2001:db8:0:0:0:0:0:1', '::ffff:1.2.3.4']) {
      expect(isIPv6(ok), ok).toBe(true)
    }
    for (const bad of ['2001:db8::1::2', '1.2.3.4', '12345::1', '1:2:3:4:5:6:7', 'g::1', '1:2:3:4:5:6:7:8:9']) {
      expect(isIPv6(bad), bad).toBe(false)
    }
  })
})

describe('validateValues', () => {
  it('needs at least one value', () => {
    expect(validateValues('A', 'www', ' \n ')).toMatch(/at least one/)
  })

  it('checks A and AAAA', () => {
    expect(validateValues('A', 'www', '203.0.113.10\n203.0.113.11')).toBeNull()
    expect(validateValues('A', 'www', '2001:db8::1')).toMatch(/IPv4/)
    expect(validateValues('AAAA', 'www', '2001:db8::1')).toBeNull()
    expect(validateValues('AAAA', 'www', '203.0.113.10')).toMatch(/IPv6/)
  })

  it('checks CNAME', () => {
    expect(validateValues('CNAME', 'www', 'target.example.com')).toBeNull()
    expect(validateValues('CNAME', 'www', 'target.example.com.')).toBeNull()
    expect(validateValues('CNAME', 'www', 'a.example.com\nb.example.com')).toMatch(/only have one/)
    expect(validateValues('CNAME', '@', 'a.example.com')).toMatch(/domain itself/)
    expect(validateValues('CNAME', 'www', 'not a host')).toMatch(/host name/)
  })

  it('checks NS', () => {
    expect(validateValues('NS', 'sub', 'ns1.example.com')).toBeNull()
    expect(validateValues('NS', '@', 'ns1.example.com')).toMatch(/domain itself/)
  })

  it('checks MX', () => {
    expect(validateValues('MX', '@', '10 mail.example.com')).toBeNull()
    expect(validateValues('MX', '@', '0 .')).toBeNull()
    expect(validateValues('MX', '@', 'mail.example.com')).toMatch(/priority/)
    expect(validateValues('MX', '@', '70000 mail.example.com')).toMatch(/priority/)
    expect(validateValues('MX', '@', '10 bad host')).toMatch(/priority/)
    expect(validateValues('MX', '@', '10 bad_host!')).toMatch(/host name/)
  })

  it('checks TXT', () => {
    expect(validateValues('TXT', '@', 'v=spf1 include:_spf.example.com ~all')).toBeNull()
    expect(validateValues('TXT', '@', '"v=spf1 ~all"')).toMatch(/without quotes/)
    expect(validateValues('TXT', '@', 'x'.repeat(2001))).toMatch(/2000/)
  })

  it('checks CAA', () => {
    expect(validateValues('CAA', '@', '0 issue "letsencrypt.org"')).toBeNull()
    expect(validateValues('CAA', '@', '0 issue letsencrypt.org')).toBeNull()
    expect(validateValues('CAA', '@', 'issue letsencrypt.org')).toMatch(/flag/)
    expect(validateValues('CAA', '@', '300 issue x')).toMatch(/flag/)
  })

  it('checks SRV', () => {
    expect(validateValues('SRV', '_sip._tcp', '10 5 5060 sip.example.com')).toBeNull()
    expect(validateValues('SRV', '_sip._tcp', '10 5 sip.example.com')).toMatch(/priority, weight/)
    expect(validateValues('SRV', '_sip._tcp', '10 5 99999 sip.example.com')).toMatch(/priority, weight/)
  })
})

describe('ttl', () => {
  it('accepts empty and the allowed range', () => {
    expect(validateTtl(null)).toBeNull()
    expect(validateTtl(60)).toBeNull()
    expect(validateTtl(86400)).toBeNull()
  })

  it('rejects values outside the range', () => {
    expect(validateTtl(59)).toMatch(/60/)
    expect(validateTtl(86401)).toMatch(/86400/)
    expect(validateTtl(100.5)).toMatch(/whole number/)
  })
})

describe('validateRecordDraft', () => {
  it('puts each message on its field', () => {
    const errors = validateRecordDraft({ name: 'bad name', type: 'A', valuesText: 'nope', ttl: 5 }, 'example.com')
    expect(Object.keys(errors).sort()).toEqual(['name', 'ttl', 'values'])
  })

  it('passes a good draft', () => {
    expect(
      validateRecordDraft({ name: 'www', type: 'A', valuesText: '203.0.113.10', ttl: null }, 'example.com'),
    ).toEqual({})
  })

  it('rejects types that are not allowed', () => {
    expect(validateRecordDraft({ name: 'x', type: 'SOA', valuesText: 'y', ttl: null }, 'example.com').type).toBeTruthy()
  })
})

describe('parseValues and server errors', () => {
  it('splits lines and drops blanks', () => {
    expect(parseValues(' a \n\n b\n')).toEqual(['a', 'b'])
  })

  it('routes server messages to a field when it is obvious', () => {
    expect(routeServerError('TTL must be between 60 and 86400.')).toEqual({ ttl: 'TTL must be between 60 and 86400.' })
    expect(routeServerError('"x" is not a valid IPv4 address.')).toHaveProperty('values')
    expect(routeServerError('Hetzner could not be reached.')).toHaveProperty('form')
  })
})
