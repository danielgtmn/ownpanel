import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { modules } from '../modules/registry.ts'
import { errorMessage, loadPage, modulePage, PageLoadError } from './lazyPage.ts'
import { PageErrorBoundary } from './ModulePage.tsx'

describe('loadPage', () => {
  it('hands the loaded component to the lazy loader', async () => {
    const Page = () => null
    await expect(loadPage(() => Promise.resolve(Page))).resolves.toEqual({ default: Page })
  })

  it('marks a failed download so it can be told apart from a broken page', async () => {
    const cause = new TypeError('Failed to fetch dynamically imported module')
    const error = await loadPage(() => Promise.reject(cause)).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(PageLoadError)
    expect((error as PageLoadError).cause).toBe(cause)
  })
})

describe('errorMessage', () => {
  it('suggests reloading after an update when a page could not be downloaded', () => {
    expect(errorMessage(new PageLoadError(new Error('gone'))).message).toContain('updated')
  })

  it('stays general for other errors and never shows their details', () => {
    const { title, message } = errorMessage(new Error('Cannot read properties of undefined'))
    expect(title).toBe('Something went wrong on this page')
    expect(message).not.toContain('undefined')
  })
})

describe('PageErrorBoundary', () => {
  function renderWith(error: unknown, frame?: string) {
    const boundary = new PageErrorBoundary({ children: 'page', onReload: () => {}, frame })
    boundary.state = { error }
    return renderToStaticMarkup(<>{boundary.render()}</>)
  }

  it('shows the page while nothing failed', () => {
    expect(renderWith(null)).toBe('page')
  })

  it('offers a reload instead of a blank page', () => {
    const html = renderWith(new PageLoadError(new Error('gone')))
    expect(html).toContain("Couldn&#x27;t open this page")
    expect(html).toMatch(/<button[^>]*>Reload<\/button>/)
  })

  it('wraps the error in the frame a page brings', () => {
    expect(renderWith(new Error('x'), 'gate')).toMatch(/^<div class="gate">/)
  })
})

describe('module pages', () => {
  it('keeps one lazy component per module, so a page keeps its state across renders', () => {
    expect(modulePage(modules[0])).toBe(modulePage(modules[0]))
    expect(modulePage(modules[0])).not.toBe(modulePage(modules[1]))
  })

  it.each(modules.map((module) => [module.id, module] as const))('%s loads its page', async (_, module) => {
    expect(typeof (await module.page())).toBe('function')
  })
})
