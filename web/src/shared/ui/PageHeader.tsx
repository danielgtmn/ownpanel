import type { ReactNode } from 'react'

type PageHeaderProps = {
  title: string
  description?: string
  back?: ReactNode
  meta?: ReactNode
  actions?: ReactNode
}

export function PageHeader({ title, description, back, meta, actions }: PageHeaderProps) {
  return (
    <header className="page-header">
      {back}
      <div className="page-header-row">
        <div className="page-header-text">
          <h1>{title}</h1>
          {description ? <p className="muted">{description}</p> : null}
        </div>
        {actions ? <div className="page-header-actions">{actions}</div> : null}
      </div>
      {meta ? <div className="page-header-meta">{meta}</div> : null}
    </header>
  )
}
