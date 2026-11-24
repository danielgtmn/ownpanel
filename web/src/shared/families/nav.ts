import type { ComponentType } from 'react'
import type { Route } from '../routing/index.ts'

export type RowStatus = 'live' | 'attention' | 'off'

export type FamilyRow = {
  key: string
  label: string
  href: string
  icon: ComponentType
  hint?: string
  detail?: string
  status?: RowStatus
  statusText?: string
  isCurrent: (route: Route) => boolean
}

export function startsWith(segments: string[], prefix: string[]): boolean {
  return prefix.every((segment, index) => segments[index] === segment)
}
