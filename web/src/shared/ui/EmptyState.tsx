import type { ReactNode } from 'react'

type EmptyStateProps = {
  title: string
  description?: string
  action?: ReactNode
  /** Inside a section: no frame of its own, and a heading below the section's. */
  inSection?: boolean
}

export function EmptyState({ title, description, action, inSection }: EmptyStateProps) {
  const Heading = inSection ? 'h3' : 'h2'
  return (
    <div className="empty-state" data-in-section={inSection ? '' : undefined}>
      <Heading>{title}</Heading>
      {description ? <p className="muted">{description}</p> : null}
      {action}
    </div>
  )
}
