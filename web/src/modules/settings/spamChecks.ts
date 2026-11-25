import type { PanelSettings, SettingsChange } from '../../shared/settings/index.ts'

export type ListMode = 'default' | 'own' | 'off'

export type SpamDraft = {
  mode: ListMode
  lists: string
  junkScore: number | null
  greylisting: boolean
}

export type SpamErrors = { lists?: string; junkScore?: string }

export const MAX_LISTS = 10
export const JUNK_MIN = 1
export const JUNK_MAX = 50

// A list with a key comes back from the server as "••••.zen.dq.spamhaus.net"; sending it back keeps the key.
const HIDDEN_KEY = '••••'

export function spamDraft(settings: PanelSettings): SpamDraft {
  const mode: ListMode = settings.mail_blocklists_default
    ? 'default'
    : settings.mail_blocklists.length === 0
      ? 'off'
      : 'own'
  return {
    mode,
    lists: settings.mail_blocklists.join('\n'),
    junkScore: settings.mail_junk_score,
    greylisting: settings.mail_greylisting,
  }
}

export function parseLists(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((name) => name.trim().toLowerCase().replace(/\.$/, ''))
    .filter(Boolean)
}

const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

function validName(name: string): boolean {
  const plain = name.startsWith(`${HIDDEN_KEY}.`) ? name.slice(HIDDEN_KEY.length + 1) : name
  const labels = plain.split('.')
  return plain.length <= 253 && labels.length >= 2 && labels.every((label) => LABEL.test(label))
}

export function validateSpamDraft(draft: SpamDraft): SpamErrors {
  const errors: SpamErrors = {}
  if (draft.mode === 'own') {
    const names = parseLists(draft.lists)
    const bad = names.find((name) => !validName(name))
    const twice = names.find((name, index) => names.indexOf(name) !== index && !name.startsWith(HIDDEN_KEY))
    if (names.length === 0) errors.lists = 'Enter at least one list, or choose not to check.'
    else if (bad) errors.lists = `“${bad}” isn't a list name. Enter names like zen.spamhaus.org, one per line.`
    else if (twice) errors.lists = `${twice} is in the list twice.`
    else if (names.length > MAX_LISTS) errors.lists = `Use at most ${MAX_LISTS} lists.`
  }
  const score = draft.junkScore
  if (score === null || !Number.isFinite(score)) errors.junkScore = 'Enter a number.'
  else if (score < JUNK_MIN || score > JUNK_MAX) errors.junkScore = `Enter a number from ${JUNK_MIN} to ${JUNK_MAX}.`
  return errors
}

// Only what differs from the saved settings is sent.
export function spamChange(draft: SpamDraft, settings: PanelSettings): SettingsChange {
  const saved = spamDraft(settings)
  const change: SettingsChange = {}
  if (draft.mode === 'default') {
    if (saved.mode !== 'default') change.mail_blocklists_default = true
  } else {
    const lists = draft.mode === 'off' ? [] : parseLists(draft.lists)
    const same = saved.mode === draft.mode && lists.join('\n') === parseLists(saved.lists).join('\n')
    if (!same) change.mail_blocklists = lists
  }
  if (draft.junkScore !== null && draft.junkScore !== settings.mail_junk_score) change.mail_junk_score = draft.junkScore
  if (draft.greylisting !== settings.mail_greylisting) change.mail_greylisting = draft.greylisting
  return change
}
