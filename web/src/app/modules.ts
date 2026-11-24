import type { PanelModule } from '../modules/types.ts'
import type { User } from '../shared/session/index.ts'

export function visibleModules(modules: PanelModule[], user: User): PanelModule[] {
  return modules.filter((module) => !module.visible || module.visible(user))
}

export function moduleFor(
  modules: PanelModule[],
  segments: string[],
): { module: PanelModule; rest: string[] } | null {
  const [first, ...rest] = segments
  const module = modules.find((candidate) => candidate.basePath.replace(/^\//, '') === first)
  return module ? { module, rest } : null
}

export function defaultModule(modules: PanelModule[]): PanelModule | undefined {
  return modules[0]
}
