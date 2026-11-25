import { validForwardAddress } from '../shared/mailrules/index.ts'
import type { FilterCondition, FilterField, FilterOp, FilterRule } from './api.ts'

// The simple filter editor. Limits are the mail server's (internal/mail/config/rules.go).
export const RULES_MAX = 50
export const CONDITIONS_MAX = 10
export const VALUE_MAX = 256
export const RULE_NAME_MAX = 64

export const FIELD_LABELS: Record<FilterField, string> = {
  from: 'From',
  to: 'To or Cc',
  subject: 'Subject',
  list: 'Mailing list',
}

export const OP_LABELS: Record<FilterOp, string> = { contains: 'contains', is: 'is' }

export const FIELDS = Object.keys(FIELD_LABELS) as FilterField[]
export const OPS = Object.keys(OP_LABELS) as FilterOp[]

let counter = 0
function nextKey(): string {
  counter += 1
  return `k${counter}`
}

export type ConditionDraft = FilterCondition & { key: string }

export type RuleDraft = {
  name: string
  matchAny: boolean
  conditions: ConditionDraft[]
  move: boolean
  folder: string
  markRead: boolean
  forward: boolean
  forwardTo: string
  delete: boolean
}

export function folderLabel(folder: string): string {
  return folder.toUpperCase() === 'INBOX' ? 'Inbox' : folder
}

// Folders a rule can move mail to; the inbox is where mail goes anyway.
export function moveTargets(folders: string[], current = ''): string[] {
  const list = folders.filter((folder) => folder.toUpperCase() !== 'INBOX')
  if (current && !list.includes(current)) list.push(current)
  return list
}

export function newCondition(): ConditionDraft {
  return { key: nextKey(), field: 'from', op: 'contains', value: '' }
}

export function newRuleDraft(folders: string[]): RuleDraft {
  return {
    name: '',
    matchAny: false,
    conditions: [newCondition()],
    move: false,
    folder: moveTargets(folders)[0] ?? '',
    markRead: false,
    forward: false,
    forwardTo: '',
    delete: false,
  }
}

export function ruleDraft(rule: FilterRule, folders: string[]): RuleDraft {
  return {
    name: rule.name,
    matchAny: rule.match_any,
    conditions: (rule.conditions.length > 0 ? rule.conditions : [{ field: 'from', op: 'contains', value: '' }]).map(
      (condition) => ({ ...condition, key: nextKey() }) as ConditionDraft,
    ),
    move: Boolean(rule.folder),
    folder: rule.folder || moveTargets(folders)[0] || '',
    markRead: Boolean(rule.mark_read),
    forward: Boolean(rule.forward_to),
    forwardTo: rule.forward_to ?? '',
    delete: Boolean(rule.delete),
  }
}

// Exactly the fields the server knows; actions only when they're on.
export function ruleFromDraft(draft: RuleDraft): FilterRule {
  const rule: FilterRule = {
    name: draft.name.trim(),
    match_any: draft.matchAny,
    conditions: draft.conditions.map(({ field, op, value }) => ({ field, op, value: value.trim() })),
  }
  if (draft.move && draft.folder && !draft.delete) rule.folder = draft.folder
  if (draft.markRead) rule.mark_read = true
  if (draft.forward && draft.forwardTo.trim()) rule.forward_to = draft.forwardTo.trim().toLowerCase()
  if (draft.delete) rule.delete = true
  return rule
}

export type RuleErrors = {
  name?: string
  conditions?: Record<string, string>
  actions?: string
  folder?: string
  forwardTo?: string
}

export function ruleErrors(draft: RuleDraft): RuleErrors {
  const errors: RuleErrors = {}
  const name = draft.name.trim()
  if ([...name].length > RULE_NAME_MAX) errors.name = `Use at most ${RULE_NAME_MAX} characters.`
  else if (/[<>"\r\n]/.test(name)) errors.name = 'Use a single line without < > and ".'

  const conditions: Record<string, string> = {}
  for (const condition of draft.conditions) {
    const value = condition.value.trim()
    if (!value) conditions[condition.key] = 'Enter what to look for.'
    else if ([...value].length > VALUE_MAX) conditions[condition.key] = `Use at most ${VALUE_MAX} characters.`
    else if (/[\r\n]/.test(value)) conditions[condition.key] = 'Use a single line.'
  }
  if (Object.keys(conditions).length > 0) errors.conditions = conditions

  if (draft.move && !draft.folder) errors.folder = 'Choose a folder.'
  if (draft.forward && !draft.forwardTo.trim()) errors.forwardTo = 'Enter the address to forward to.'
  else if (draft.forward && !validForwardAddress(draft.forwardTo)) {
    errors.forwardTo = 'Enter a whole e-mail address, like anna@example.com.'
  }
  if (draft.conditions.length === 0) errors.actions = 'Add at least one condition.'
  else if (!draft.move && !draft.markRead && !draft.forward && !draft.delete) {
    errors.actions = 'Choose at least one thing to do with the mail.'
  } else if (draft.move && draft.delete) errors.actions = 'Mail can be moved or deleted, not both.'
  return errors
}

export function hasRuleErrors(errors: RuleErrors): boolean {
  return Boolean(errors.name || errors.conditions || errors.actions || errors.folder || errors.forwardTo)
}

export function addCondition(conditions: ConditionDraft[]): ConditionDraft[] {
  return conditions.length >= CONDITIONS_MAX ? conditions : [...conditions, newCondition()]
}

// ---- the list of rules ----

export type KeyedRule = { key: string; rule: FilterRule }

export function keyRules(rules: FilterRule[]): KeyedRule[] {
  return rules.map((rule) => ({ key: nextKey(), rule }))
}

export function moveRule(list: KeyedRule[], index: number, by: -1 | 1): KeyedRule[] {
  const to = index + by
  if (index < 0 || index >= list.length || to < 0 || to >= list.length) return list
  const next = [...list]
  ;[next[index], next[to]] = [next[to], next[index]]
  return next
}

export function removeRule(list: KeyedRule[], key: string): KeyedRule[] {
  return list.filter((entry) => entry.key !== key)
}

// Replaces the rule with the key, or adds it at the end.
export function putRule(list: KeyedRule[], key: string | null, rule: FilterRule): KeyedRule[] {
  if (key && list.some((entry) => entry.key === key)) {
    return list.map((entry) => (entry.key === key ? { key, rule } : entry))
  }
  return [...list, { key: nextKey(), rule }]
}

export function rulesBody(list: KeyedRule[]): FilterRule[] {
  return list.map((entry) => entry.rule)
}

export function sameRules(a: FilterRule[], b: FilterRule[]): boolean {
  return JSON.stringify(a) === JSON.stringify(b)
}

const FIELD_WORDS: Record<FilterField, string> = { from: 'from', to: 'to or Cc', subject: 'subject', list: 'mailing list' }

function conditionText(condition: FilterCondition): string {
  return `${FIELD_WORDS[condition.field] ?? condition.field} ${OP_LABELS[condition.op] ?? condition.op} “${condition.value}”`
}

// "From contains “shop”" and "Move to Archive, mark as read", for the list.
export function describeRule(rule: FilterRule): { when: string; then: string } {
  const when = rule.conditions.map(conditionText).join(rule.match_any ? ' or ' : ' and ')
  const actions: string[] = []
  if (rule.delete) actions.push('Delete')
  if (rule.folder) actions.push(`Move to ${folderLabel(rule.folder)}`)
  if (rule.mark_read) actions.push(actions.length > 0 ? 'mark as read' : 'Mark as read')
  if (rule.forward_to) actions.push(actions.length > 0 ? `forward to ${rule.forward_to}` : `Forward to ${rule.forward_to}`)
  return { when: `When ${when}`, then: actions.join(', ') }
}

export function ruleTitle(rule: FilterRule, index: number): string {
  return rule.name || `Rule ${index + 1}`
}
