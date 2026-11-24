import { describe, expect, it } from 'vitest'
import {
  addressOptions,
  certificateText,
  checksSettled,
  dnsText,
  dnsTone,
  domainChangeQuestion,
  initialAddressSelection,
  normalizeDomain,
  panelReady,
  panelUrl,
  recordRows,
  sameAddresses,
  validateAddress,
  validateDomain,
  validateEmail,
} from './rules.ts'
import { checksFixture, settingsFixture } from './testing.ts'

describe('recordRows', () => {
  it('uses an A record for IPv4 and an AAAA record for IPv6, IPv4 first', () => {
    expect(recordRows('panel.example.com', ['2001:db8::5', '203.0.113.5'])).toEqual([
      { type: 'A', name: 'panel.example.com', value: '203.0.113.5' },
      { type: 'AAAA', name: 'panel.example.com', value: '2001:db8::5' },
    ])
  })

  it('has no rows without a domain or without addresses', () => {
    expect(recordRows('', ['203.0.113.5'])).toEqual([])
    expect(recordRows('panel.example.com', [])).toEqual([])
  })
})

describe('check texts', () => {
  it('describes where the domain points', () => {
    expect(dnsText(checksFixture('points_here', 'ready').dns)).toBe('Points to this server')
    expect(dnsText(checksFixture('points_elsewhere', 'pending', ['198.51.100.7']).dns)).toBe(
      'Not pointing here yet (found 198.51.100.7)',
    )
    expect(dnsText(checksFixture('points_elsewhere', 'pending').dns)).toBe('Not pointing here yet')
    expect(dnsText(checksFixture('no_record', 'pending').dns)).toBe('No record yet')
    expect(dnsText(checksFixture('lookup_failed', 'pending').dns)).toMatch(/couldn't be checked/i)
  })

  it('describes the certificate', () => {
    expect(certificateText(checksFixture('points_here', 'ready').certificate)).toBe('Ready')
    expect(certificateText(checksFixture('no_record', 'pending').certificate)).toBe('Waiting for the certificate')
    expect(certificateText(checksFixture('points_here', 'not_applicable').certificate)).toBe('Not needed')
  })

  it('marks only a working domain as positive', () => {
    expect(dnsTone(checksFixture('points_here', 'ready').dns)).toBe('positive')
    expect(dnsTone(checksFixture('no_record', 'pending').dns)).toBe('warning')
    expect(dnsTone(checksFixture('lookup_failed', 'pending').dns)).toBe('neutral')
  })
})

describe('readiness', () => {
  it('is ready when DNS points here and the certificate is there or not needed', () => {
    expect(panelReady(checksFixture('points_here', 'ready'))).toBe(true)
    expect(panelReady(checksFixture('points_here', 'not_applicable'))).toBe(true)
    expect(panelReady(checksFixture('points_here', 'pending'))).toBe(false)
    expect(panelReady(checksFixture('no_record', 'ready'))).toBe(false)
  })

  it('stops waiting when ready or when there is no domain', () => {
    expect(checksSettled(checksFixture('points_here', 'ready'))).toBe(true)
    expect(checksSettled(checksFixture('not_configured', 'not_configured'))).toBe(true)
    expect(checksSettled(checksFixture('points_here', 'pending'))).toBe(false)
  })
})

describe('addresses', () => {
  it('preselects public detected addresses when none are saved', () => {
    expect(initialAddressSelection(settingsFixture())).toEqual(['203.0.113.5', '2001:db8::5'])
  })

  it('keeps saved addresses', () => {
    expect(initialAddressSelection(settingsFixture({ server_ips: ['198.51.100.1'] }))).toEqual(['198.51.100.1'])
  })

  it('lists detected, saved and added addresses once each', () => {
    const options = addressOptions(settingsFixture({ server_ips: ['203.0.113.5', '198.51.100.1'] }), ['198.51.100.2'])
    expect(options.map((option) => option.ip)).toEqual([
      '203.0.113.5',
      '2001:db8::5',
      '192.168.1.20',
      '198.51.100.1',
      '198.51.100.2',
    ])
    expect(options.find((option) => option.ip === '192.168.1.20')?.public).toBe(false)
  })

  it('checks a typed address', () => {
    expect(validateAddress('203.0.113.9')).toBeNull()
    expect(validateAddress('[2001:db8::9]')).toBeNull()
    expect(validateAddress('')).toMatch(/enter an ip address/i)
    expect(validateAddress('300.1.1.1')).toMatch(/like 203.0.113.5/)
    expect(validateAddress('example.com')).toMatch(/like 203.0.113.5/)
    expect(validateAddress('203.0.113.5', ['203.0.113.5'])).toMatch(/already/)
  })

  it('compares address lists regardless of order', () => {
    expect(sameAddresses(['a', 'b'], ['b', 'a'])).toBe(true)
    expect(sameAddresses(['a'], ['a', 'b'])).toBe(false)
  })
})

describe('domain and e-mail', () => {
  it('accepts a pasted address and keeps only the domain', () => {
    expect(normalizeDomain(' https://Panel.Example.com/login ')).toBe('panel.example.com')
    expect(validateDomain('https://panel.example.com/')).toBeNull()
  })

  it('rejects empty values, IP addresses and odd characters', () => {
    expect(validateDomain('')).toMatch(/enter a domain/i)
    expect(validateDomain('203.0.113.5')).toMatch(/not an IP address/)
    expect(validateDomain('panel')).toMatch(/like panel.example.com/)
    expect(validateDomain('my panel.example.com')).toMatch(/without spaces/)
  })

  it('treats the e-mail as optional', () => {
    expect(validateEmail('')).toBeNull()
    expect(validateEmail('you@example.com')).toBeNull()
    expect(validateEmail('you@')).toMatch(/e-mail address/)
  })

  it('warns that the old address stops working', () => {
    expect(domainChangeQuestion('old.example.com', 'new.example.com').description).toMatch(
      /old.example.com stops working once new.example.com is ready/,
    )
    expect(domainChangeQuestion('old.example.com', '').title).toMatch(/remove/i)
  })
})

describe('panelUrl', () => {
  it('adds a port only when it is not the default', () => {
    expect(panelUrl('panel.example.com')).toBe('https://panel.example.com')
    expect(panelUrl('panel.example.com', '443')).toBe('https://panel.example.com')
    expect(panelUrl('panel.example.com', '8443')).toBe('https://panel.example.com:8443')
  })
})
