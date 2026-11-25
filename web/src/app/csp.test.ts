import { describe, expect, it } from 'vitest'
import html from '../../index.html?raw'

// The panel's Content Security Policy (internal/webui) has no nonce and no
// 'unsafe-inline' for styles. ui-sdk only stays within it when index.html
// carries an empty csp-nonce tag: then it adds its CSS as constructed style
// sheets instead of <style> elements.
describe('index.html', () => {
  it('asks ui-sdk for constructed style sheets', () => {
    expect(html).toMatch(/<meta property="csp-nonce"\s*\/?>/)
  })

  it('has no inline script or style', () => {
    expect(html).not.toMatch(/<script(?![^>]*\bsrc=)[^>]*>/)
    expect(html).not.toMatch(/<style[\s>]/)
    expect(html).not.toMatch(/\sstyle=/)
  })
})
