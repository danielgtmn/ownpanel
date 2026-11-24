import type { ReactNode } from 'react'

type StatusBadgeProps = {
  tone: 'positive' | 'neutral' | 'warning'
  children: ReactNode
}

export function StatusBadge({ tone, children }: StatusBadgeProps) {
  return (
    <span className="status-badge" data-tone={tone}>
      <span className="status-dot" aria-hidden="true" />
      {children}
    </span>
  )
}
