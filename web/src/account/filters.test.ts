import { describe, expect, it } from 'vitest'
import type { FilterRule } from './api.ts'
import {
  addCondition,
  CONDITIONS_MAX,
  describeRule,
  hasRuleErrors,
  keyRules,
  moveRule,
  moveTargets,
  newRuleDraft,
  putRule,
  removeRule,
  ruleDraft,
  ruleErrors,
  ruleFromDraft,
  rulesBody,
  sameRules,
  type RuleDraft,
} from './filters.ts'

const folders = ['INBOX', 'Archive', 'Sent', 'Newsletters']

function draft(change: Partial<RuleDraft> = {}): RuleDraft {
  const base = newRuleDraft(folders)
  base.conditions[0].value = 'news@example.com'
  return { ...base, ...change }
}

describe('a filter rule', () => {
  it('starts with one condition and the first folder that isn’t the inbox', () => {
    const fresh = newRuleDraft(folders)
    expect(fresh.conditions).toHaveLength(1)
    expect(fresh.folder).toBe('Archive')
    expect(moveTargets(folders)).toEqual(['Archive', 'Sent', 'Newsletters'])
    expect(moveTargets(folders, 'Gone')).toContain('Gone')
  })

  it('needs a value in every condition and something to do', () => {
    const empty = newRuleDraft(folders)
    const errors = ruleErrors(empty)
    expect(errors.conditions?.[empty.conditions[0].key]).toBeTruthy()
    expect(errors.actions).toMatch(/at least one thing/)
    expect(hasRuleErrors(ruleErrors(draft({ markRead: true })))).toBe(false)
  })

  it('moves or deletes, never both', () => {
    expect(ruleErrors(draft({ move: true, delete: true })).actions).toMatch(/not both/)
    expect(ruleErrors(draft({ delete: true })).actions).toBeUndefined()
  })

  it('checks the forwarding address and the name like the server', () => {
    expect(ruleErrors(draft({ forward: true, forwardTo: 'anna' })).forwardTo).toBeTruthy()
    expect(ruleErrors(draft({ forward: true, forwardTo: 'anna@example.com' })).forwardTo).toBeUndefined()
    expect(ruleErrors(draft({ markRead: true, name: 'a <b>' })).name).toBeTruthy()
    expect(ruleErrors(draft({ markRead: true, name: 'x'.repeat(65) })).name).toBeTruthy()
    const long = draft({ markRead: true })
    long.conditions[0].value = 'x'.repeat(257)
    expect(ruleErrors(long).conditions).toBeTruthy()
  })

  it('sends exactly the fields the server knows, actions only when on', () => {
    const rule = ruleFromDraft(
      draft({ name: ' News ', move: true, folder: 'Newsletters', markRead: true, forward: false, forwardTo: 'x@y.z' }),
    )
    expect(rule).toEqual({
      name: 'News',
      match_any: false,
      conditions: [{ field: 'from', op: 'contains', value: 'news@example.com' }],
      folder: 'Newsletters',
      mark_read: true,
    })
    expect(ruleFromDraft(draft({ forward: true, forwardTo: ' Anna@Example.com ' })).forward_to).toBe('anna@example.com')
    expect(ruleFromDraft(draft({ delete: true })).delete).toBe(true)
  })

  it('reads a saved rule back into the editor', () => {
    const saved: FilterRule = {
      name: 'Shop',
      match_any: true,
      conditions: [
        { field: 'subject', op: 'is', value: 'Order' },
        { field: 'list', op: 'contains', value: 'shop' },
      ],
      forward_to: 'b@example.com',
    }
    const back = ruleDraft(saved, folders)
    expect(back.matchAny).toBe(true)
    expect(back.forward).toBe(true)
    expect(back.move).toBe(false)
    expect(new Set(back.conditions.map((condition) => condition.key)).size).toBe(2)
    expect(ruleFromDraft(back)).toEqual(saved)
  })

  it('stops adding conditions at the limit', () => {
    let conditions = newRuleDraft(folders).conditions
    for (let i = 0; i < 20; i++) conditions = addCondition(conditions)
    expect(conditions).toHaveLength(CONDITIONS_MAX)
  })

  it('describes itself for the list', () => {
    expect(
      describeRule({
        name: '',
        match_any: true,
        conditions: [
          { field: 'from', op: 'contains', value: 'shop' },
          { field: 'to', op: 'is', value: 'me@example.com' },
        ],
        folder: 'INBOX',
        mark_read: true,
      }),
    ).toEqual({ when: 'When from contains “shop” or to or Cc is “me@example.com”', then: 'Move to Inbox, mark as read' })
  })
})

describe('the list of rules', () => {
  const rules: FilterRule[] = ['a', 'b', 'c'].map((name) => ({
    name,
    match_any: false,
    conditions: [{ field: 'from', op: 'is', value: name }],
    mark_read: true,
  }))

  it('moves rules up and down within the list', () => {
    const list = keyRules(rules)
    expect(rulesBody(moveRule(list, 0, 1)).map((rule) => rule.name)).toEqual(['b', 'a', 'c'])
    expect(rulesBody(moveRule(list, 2, -1)).map((rule) => rule.name)).toEqual(['a', 'c', 'b'])
    expect(moveRule(list, 0, -1)).toBe(list)
    expect(moveRule(list, 2, 1)).toBe(list)
  })

  it('removes, replaces and adds by key', () => {
    const list = keyRules(rules)
    expect(rulesBody(removeRule(list, list[1].key)).map((rule) => rule.name)).toEqual(['a', 'c'])
    const changed = putRule(list, list[0].key, { ...rules[0], name: 'z' })
    expect(rulesBody(changed).map((rule) => rule.name)).toEqual(['z', 'b', 'c'])
    expect(changed[0].key).toBe(list[0].key)
    expect(rulesBody(putRule(list, null, { ...rules[0], name: 'd' }))).toHaveLength(4)
    expect(sameRules(rulesBody(list), rules)).toBe(true)
    expect(sameRules(rulesBody(changed), rules)).toBe(false)
  })
})
