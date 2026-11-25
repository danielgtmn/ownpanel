import { Component, createElement, Suspense, type ReactNode } from 'react'
import type { PanelModule } from '../modules/types.ts'
import { ErrorState, LoadingState } from '../shared/ui/index.ts'
import { errorMessage, modulePage } from './lazyPage.ts'

type BoundaryProps = { children: ReactNode; onReload: () => void; frame?: string }

export class PageErrorBoundary extends Component<BoundaryProps, { error: unknown }> {
  state: { error: unknown } = { error: null }

  static getDerivedStateFromError(error: unknown) {
    return { error }
  }

  render() {
    if (this.state.error === null) return this.props.children
    const { title, message } = errorMessage(this.state.error)
    return (
      <Framed className={this.props.frame}>
        <ErrorState title={title} message={message} onRetry={this.props.onReload} retryLabel="Reload" />
      </Framed>
    )
  }
}

function Framed({ className, children }: { className?: string; children: ReactNode }) {
  return className ? <div className={className}>{children}</div> : children
}

const reload = () => window.location.reload()

// The loading state while a page downloads and an error with a reload action
// if that fails. `frame` is a class for the element around those two states,
// for pages that bring their own layout (the setup wizard's `gate`). Give it
// a `key` per page so a failure doesn't stick to the next one.
export function PageFrame({ children, frame }: { children: ReactNode; frame?: string }) {
  return (
    <PageErrorBoundary onReload={reload} frame={frame}>
      <Suspense
        fallback={
          <Framed className={frame}>
            <LoadingState />
          </Framed>
        }
      >
        {children}
      </Suspense>
    </PageErrorBoundary>
  )
}

// Shows a module's page, downloading its code the first time it is opened.
export function ModulePage({ module, segments }: { module: PanelModule; segments: string[] }) {
  // modulePage returns the same component for a module every time.
  return <PageFrame>{createElement(modulePage(module), { segments })}</PageFrame>
}
