import type { ComponentType } from 'react'
import type { User } from '../shared/session/index.ts'

export type ModuleProps = {
  segments: string[]
}

export type PanelModule = {
  id: string
  label: string
  basePath: string
  component: ComponentType<ModuleProps>
  visible?: (user: User) => boolean
}
