import { toast } from '@ui-sdk/react/toast'

export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success('Copied.')
  } catch {
    toast.error("Couldn't copy. Select the text and copy it yourself.")
  }
}
