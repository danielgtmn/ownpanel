import { describe, expect, it } from 'vitest'
import { suggestedPreviewDomain, validatePreviewDomain } from './previews.ts'

describe('preview domain', () => {
  it('suggests a name below the panel address', () => {
    expect(suggestedPreviewDomain({ panel_domain: 'panel.example.com' })).toBe('preview.panel.example.com')
    expect(suggestedPreviewDomain({ panel_domain: '' })).toBe('')
  })

  it('accepts a domain or nothing', () => {
    const settings = { panel_domain: 'panel.example.com' }
    expect(validatePreviewDomain('', settings)).toBeNull()
    expect(validatePreviewDomain('Preview.Example.NET', settings)).toBeNull()
    expect(validatePreviewDomain('preview.panel.example.com', settings)).toBeNull()
  })

  it('refuses wildcards, addresses and the panel itself', () => {
    const settings = { panel_domain: 'panel.example.com' }
    expect(validatePreviewDomain('*.preview.example.net', settings)).toMatch(/without "\*\."/)
    expect(validatePreviewDomain('203.0.113.5', settings)).toMatch(/not an IP address/)
    expect(validatePreviewDomain('panel.example.com', settings)).toMatch(/panel's own address/)
    expect(validatePreviewDomain('example.com', settings)).toMatch(/panel's own address/)
    expect(validatePreviewDomain('no spaces.com', settings)).toMatch(/preview\.example\.com/)
  })
})
