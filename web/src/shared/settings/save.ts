import { toast } from '@ui-sdk/react/toast'
import { isSavedButNotApplied } from '../api/index.ts'
import { finishSetup, getSettings, updateSettings, type PanelSettings, type SettingsChange } from './api.ts'

export const NOT_APPLIED_MESSAGE =
  "Saved. The server couldn't switch over yet and keeps trying in the background; this usually sorts itself out within a minute."

export type SaveResult = { settings: PanelSettings; applied: boolean }

// A 502 means the change is stored but not yet in effect; read the stored state back instead of failing.
async function keepSaved(run: () => Promise<PanelSettings>): Promise<SaveResult> {
  try {
    return { settings: await run(), applied: true }
  } catch (error) {
    if (!isSavedButNotApplied(error)) throw error
    return { settings: await getSettings(), applied: false }
  }
}

export function saveSettings(change: SettingsChange): Promise<SaveResult> {
  return keepSaved(() => updateSettings(change))
}

export function finishWizard(): Promise<SaveResult> {
  return keepSaved(finishSetup)
}

export function warnIfNotApplied(result: SaveResult) {
  if (!result.applied) toast.warning(NOT_APPLIED_MESSAGE)
}

export function announceSaved(result: SaveResult, message: string) {
  if (result.applied) toast.success(message)
  else warnIfNotApplied(result)
}

export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success('Copied.')
  } catch {
    toast.error("Couldn't copy. Select the text and copy it yourself.")
  }
}
