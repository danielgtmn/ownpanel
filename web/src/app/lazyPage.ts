import { lazy, type ComponentType } from 'react'
import type { ModuleProps, PanelModule } from '../modules/types.ts'

// A page's code that couldn't be downloaded, typically because the panel was
// updated and the old files are gone. Reloading fetches the current ones.
export class PageLoadError extends Error {
  constructor(cause: unknown) {
    super("This page couldn't be loaded.", { cause })
    this.name = 'PageLoadError'
  }
}

export function loadPage<P>(load: () => Promise<ComponentType<P>>): Promise<{ default: ComponentType<P> }> {
  return load().then(
    (component) => ({ default: component }),
    (error: unknown) => {
      throw new PageLoadError(error)
    },
  )
}

// A component whose code is downloaded the first time it renders. Render it
// inside <PageFrame>, which shows the loading state and catches a failed download.
export function lazyPage<P>(load: () => Promise<ComponentType<P>>): ComponentType<P> {
  return lazy(() => loadPage(load)) as unknown as ComponentType<P>
}

// React.lazy has to see the same component on every render, so there is one per module.
const pages = new WeakMap<PanelModule, ComponentType<ModuleProps>>()

export function modulePage(module: PanelModule): ComponentType<ModuleProps> {
  let page = pages.get(module)
  if (!page) {
    page = lazyPage(module.page)
    pages.set(module, page)
  }
  return page
}

export function errorMessage(error: unknown): { title: string; message: string } {
  if (error instanceof PageLoadError) {
    return {
      title: "Couldn't open this page",
      message: 'The panel may have been updated in the meantime. Reload to continue with the current version.',
    }
  }
  return { title: 'Something went wrong on this page', message: 'Reload to try again.' }
}
