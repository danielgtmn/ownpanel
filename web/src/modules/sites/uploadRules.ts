import { type FileRejection } from '@ui-sdk/react/file-input'

export const MAX_BYTES = 512 * 1024 * 1024
export const ACCEPT = ['.zip', '.tar.gz', '.tgz']

export function rejectionMessage(rejected: FileRejection[]): string {
  const [first] = rejected
  if (!first) return ''
  if (rejected.length > 1 || first.reasons.includes('too-many-files')) {
    return 'Choose just one file.'
  }
  if (first.reasons.includes('file-too-large')) {
    return `"${first.file.name}" is too large. The limit is 512 MB.`
  }
  if (first.reasons.includes('file-type')) {
    return `"${first.file.name}" can't be published. Choose a .zip, .tar.gz or .tgz file.`
  }
  return `"${first.file.name}" can't be used.`
}
