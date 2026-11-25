import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { AddressPicker } from './AddressPicker.tsx'
import { CertificateEmailForm } from './CertificateEmailForm.tsx'
import { PanelDomainForm } from './PanelAddress.tsx'
import { settingsFixture } from './testing.ts'

const noop = () => undefined

describe('locked fields', () => {
  it('shows a domain from the server configuration read-only, without a save button', () => {
    const html = renderToStaticMarkup(
      <PanelDomainForm
        settings={settingsFixture({ panel_domain: 'panel.example.com', panel_domain_locked: true })}
        onSaved={noop}
      />,
    )
    expect(html).toContain('value="panel.example.com"')
    expect(html).toMatch(/readonly/i)
    expect(html).toContain('Set in the server configuration')
    expect(html).not.toContain('Save domain')
  })

  it('shows locked addresses instead of checkboxes', () => {
    const html = renderToStaticMarkup(
      <AddressPicker
        settings={settingsFixture({ server_ips: ['203.0.113.5', '2001:db8::5'], server_ips_locked: true })}
        selected={[]}
        onSelectedChange={noop}
        extra={[]}
        onExtraChange={noop}
      />,
    )
    expect(html).toContain('value="203.0.113.5, 2001:db8::5"')
    expect(html).toContain('Set in the server configuration')
    expect(html).not.toContain('type="checkbox"')
  })

  it('shows a locked e-mail read-only', () => {
    const html = renderToStaticMarkup(
      <CertificateEmailForm settings={settingsFixture({ acme_email: 'ops@example.com', acme_email_locked: true })} onSaved={noop} />,
    )
    expect(html).toContain('value="ops@example.com"')
    expect(html).toMatch(/readonly/i)
    expect(html).not.toContain('Save e-mail')
  })

  it('offers editing when nothing is locked', () => {
    const html = renderToStaticMarkup(<PanelDomainForm settings={settingsFixture()} onSaved={noop} />)
    expect(html).toContain('Save domain')
    expect(html).not.toContain('Set in the server configuration')
  })

  it('puts private addresses into their own collapsed group', () => {
    const settings = settingsFixture()
    const html = renderToStaticMarkup(
      <AddressPicker
        settings={settings}
        selected={['203.0.113.5']}
        onSelectedChange={noop}
        extra={[]}
        onExtraChange={noop}
      />,
    )
    expect(html).toMatch(/Private addresses \(\d+\)/)
  })
})
