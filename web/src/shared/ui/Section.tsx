import { useId, type ReactNode } from 'react'

type SectionProps = {
  title: string
  description?: ReactNode
  actions?: ReactNode
  tone?: 'default' | 'danger'
  children: ReactNode
}

export function Section({ title, description, actions, tone = 'default', children }: SectionProps) {
  const titleId = useId()
  return (
    <section className="section" data-tone={tone} aria-labelledby={titleId}>
      <div className="section-head">
        <div>
          <h2 id={titleId}>{title}</h2>
          {description ? <p className="muted">{description}</p> : null}
        </div>
        {actions ? <div className="section-actions">{actions}</div> : null}
      </div>
      <div className="section-body">{children}</div>
    </section>
  )
}
