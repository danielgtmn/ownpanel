import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { StepIndicator } from './StepIndicator.tsx'

describe('StepIndicator', () => {
  it('marks the current step without repeating it as a count', () => {
    const html = renderToStaticMarkup(<StepIndicator current="panel" />)
    expect(html).toMatch(/<li data-state="current" aria-current="step">.*Panel address/)
    expect(html).toContain('Server addresses<span class="visually-hidden"> (done)</span>')
    expect(html).not.toMatch(/Step \d of/)
  })
})
