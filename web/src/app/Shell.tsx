import { useEffect, useMemo, useRef } from 'react'
import { EmptyState } from '../shared/ui/index.ts'
import { hrefFor, navigate, usePathSegments } from '../shared/routing/index.ts'
import { useSession } from '../shared/session/index.ts'
import { modules } from '../modules/registry.ts'
import { AccountMenu } from './AccountMenu.tsx'
import { defaultModule, moduleFor, visibleModules } from './modules.ts'

export function Shell() {
  const segments = usePathSegments()
  const { user } = useSession()
  const available = useMemo(() => visibleModules(modules, user), [user])
  const match = moduleFor(available, segments)
  const mainRef = useRef<HTMLElement>(null)
  const firstRender = useRef(true)
  const pathKey = segments.join('/')

  useEffect(() => {
    if (!match && segments.length === 0) {
      const first = defaultModule(available)
      if (first) navigate(first.basePath.replace(/^\//, ''))
    }
  })

  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false
      return
    }
    mainRef.current?.focus()
  }, [pathKey])

  return (
    <div className="shell">
      <header className="shell-header">
        <div className="shell-bar">
          <a className="brand" href={hrefFor()}>
            ownpanel
          </a>
          <nav className="shell-nav" aria-label="Main">
            {available.map((module) => {
              const current = match?.module.id === module.id
              return (
                <a
                  key={module.id}
                  href={'#' + module.basePath}
                  aria-current={current ? 'page' : undefined}
                >
                  {module.label}
                </a>
              )
            })}
          </nav>
          <AccountMenu />
        </div>
      </header>
      <main className="shell-main" ref={mainRef} tabIndex={-1}>
        {match ? (
          <match.module.component key={match.module.id} segments={match.rest} />
        ) : segments.length > 0 ? (
          <EmptyState
            title="Page not found"
            description="This page doesn't exist."
            action={<a href={'#' + (defaultModule(available)?.basePath ?? '/')}>Go to the start page</a>}
          />
        ) : null}
      </main>
    </div>
  )
}
