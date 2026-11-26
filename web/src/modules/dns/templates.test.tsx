import { Dialog } from '@ui-sdk/react/dialog'
import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { applyTemplate, listTemplates, templatePath } from './api.ts'
import { TemplateDialog } from './TemplateDialog.tsx'
import { actionLabel, cleanParams, missingParams, summarize, summaryText, valueDiff } from './templates.ts'
import type { RecordTemplate, TemplateChange } from './types.ts'

const verification: RecordTemplate = {
  id: 'domain-verification',
  title: 'Confirm that you own this domain',
  description: '',
  own_mail: false,
  params: [
    { id: 'value', label: 'Confirmation text', description: '', placeholder: '', optional: false },
    { id: 'name', label: 'Name', description: '', placeholder: '@', optional: true },
  ],
}

const change = (action: TemplateChange['action'], current: string[], next: string[]): TemplateChange => ({
  name: '@',
  type: 'TXT',
  action,
  current,
  new: next,
  note: '',
})

describe('record templates', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('asks only for the values a template needs', () => {
    expect(missingParams(verification, {})).toEqual({ value: 'Enter the confirmation text.' })
    expect(missingParams(verification, { value: '  x ' })).toEqual({})
    expect(cleanParams(verification, { value: ' x ', name: ' ' })).toEqual({ value: 'x' })
  })

  it('shows which values leave, come and stay', () => {
    expect(valueDiff(change('change', ['a', 'v=spf1 mx -all'], ['a', 'v=spf1 include:x mx -all']))).toEqual({
      removed: ['v=spf1 mx -all'],
      added: ['v=spf1 include:x mx -all'],
      kept: ['a'],
    })
  })

  it('sums up the plan in words', () => {
    expect(summaryText(summarize([change('ok', ['a'], ['a'])]))).toBe('Everything is already set up. Nothing needs to change.')
    expect(summaryText(summarize([change('add', [], ['a']), change('change', ['b'], ['c'])]))).toBe(
      '2 records will be added or changed.',
    )
    expect(summaryText(summarize([change('add', [], ['a']), change('conflict', ['b'], ['b'])]))).toMatch(/^One record can't/)
    expect(actionLabel('conflict')).toBe('Needs you')
  })

  it('previews without confirming', async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => new Response(JSON.stringify({ template: 't', zone: 'example.com', changes: [], applied: false })))
    vi.stubGlobal('fetch', fetchMock)
    await applyTemplate('3', 'example.com', 'domain-verification', { value: 'x' }, false)
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toContain(templatePath('3', 'example.com', 'domain-verification'))
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ params: { value: 'x' }, confirm: false })
    fetchMock.mockResolvedValueOnce(new Response('[]'))
    await listTemplates()
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('/dns/templates')
  })

  it('renders the dialog with its title', () => {
    const html = renderToStaticMarkup(
      <Dialog open>
        <TemplateDialog open onOpenChange={() => undefined} connectionId="1" zone="example.com" onApplied={async () => undefined} />
      </Dialog>,
    )
    expect(html).toContain('Set up a service')
  })
})
