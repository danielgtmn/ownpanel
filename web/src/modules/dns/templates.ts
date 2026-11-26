import type { RecordTemplate, TemplateAction, TemplateChange } from './types.ts'

export function actionLabel(action: TemplateAction): string {
  switch (action) {
    case 'add':
      return 'New'
    case 'change':
      return 'Changes'
    case 'ok':
      return 'Already set'
    case 'conflict':
      return 'Needs you'
  }
}

export function actionTone(action: TemplateAction): 'positive' | 'neutral' | 'warning' {
  if (action === 'ok') return 'positive'
  if (action === 'conflict') return 'warning'
  return 'neutral'
}

// Values that leave the record set and values that come in, so a person sees what changes.
export function valueDiff(change: TemplateChange): { removed: string[]; added: string[]; kept: string[] } {
  return {
    removed: change.current.filter((value) => !change.new.includes(value)),
    added: change.new.filter((value) => !change.current.includes(value)),
    kept: change.new.filter((value) => change.current.includes(value)),
  }
}

export type PlanSummary = { writes: number; conflicts: number }

export function summarize(changes: TemplateChange[]): PlanSummary {
  return {
    writes: changes.filter((change) => change.action === 'add' || change.action === 'change').length,
    conflicts: changes.filter((change) => change.action === 'conflict').length,
  }
}

export function summaryText({ writes, conflicts }: PlanSummary): string {
  if (conflicts > 0) {
    return conflicts === 1
      ? "One record can't be set up for you. Nothing is changed until it's sorted out."
      : `${conflicts} records can't be set up for you. Nothing is changed until they're sorted out.`
  }
  if (writes === 0) return 'Everything is already set up. Nothing needs to change.'
  return writes === 1 ? 'One record will be added or changed.' : `${writes} records will be added or changed.`
}

export type ParamErrors = Record<string, string>

export function missingParams(template: RecordTemplate, values: Record<string, string>): ParamErrors {
  const errors: ParamErrors = {}
  for (const param of template.params) {
    if (!param.optional && !(values[param.id] ?? '').trim()) errors[param.id] = `Enter the ${param.label.toLowerCase()}.`
  }
  return errors
}

export function cleanParams(template: RecordTemplate, values: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const param of template.params) {
    const value = (values[param.id] ?? '').trim()
    if (value) out[param.id] = value
  }
  return out
}
