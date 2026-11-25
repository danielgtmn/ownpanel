import { describe, expect, it } from 'vitest'
import { ApiError } from '../../shared/api/index.ts'
import {
  fileProblem,
  fitsQuota,
  folderNote,
  initialSelection,
  isActive,
  jobCounts,
  jobShare,
  jobState,
  kindSelected,
  MAX_IMPORT_BYTES,
  MAX_PART_BYTES,
  needsPassword,
  providerHint,
  quotaText,
  retriable,
  selectionTotals,
  suggestServer,
  uploadInParts,
  validateSource,
  withKind,
} from './transfer.ts'
import type { ImportCheck } from './types.ts'

const MB = 1024 ** 2

describe('validateSource', () => {
  const good = { host: 'imap.example.org', port: 993, username: 'me@example.org', password: 'secret' }

  it('accepts a complete form', () => {
    expect(validateSource(good)).toEqual({})
    expect(validateSource({ ...good, host: ' IMAP.Example.org. ' })).toEqual({})
  })

  it('asks for every field', () => {
    const errors = validateSource({ host: '', port: 993, username: ' ', password: '' })
    expect(errors.host).toMatch(/name of the other mail server/)
    expect(errors.username).toBeTruthy()
    expect(errors.password).toBeTruthy()
  })

  it('explains common mistakes in the server name', () => {
    expect(validateSource({ ...good, host: 'me@example.org' }).host).toMatch(/not an e-mail address/)
    expect(validateSource({ ...good, host: 'imaps://imap.example.org' }).host).toMatch(/only the server name/)
    expect(validateSource({ ...good, host: 'imap example.org' }).host).toMatch(/doesn't look like/)
    expect(validateSource({ ...good, host: 'outlook.office365.com' }).host).toMatch(/Microsoft 365/)
  })

  it('refuses line breaks in the user name and password', () => {
    expect(validateSource({ ...good, username: 'me\r\nx' }).username).toBeTruthy()
    expect(validateSource({ ...good, password: 'a\nb' }).password).toBeTruthy()
  })
})

describe('providers', () => {
  it('suggests the server of well-known providers', () => {
    expect(suggestServer('Someone@Gmail.com')).toBe('imap.gmail.com')
    expect(suggestServer('a@yahoo.de')).toBe('imap.mail.yahoo.com')
    expect(suggestServer('a@gmx.de')).toBe('imap.gmx.net')
    expect(suggestServer('a@example.org')).toBe('')
  })

  it('tells who needs an app password and who is not supported', () => {
    expect(providerHint('a@gmail.com', '')).toMatch(/app password/)
    expect(providerHint('', 'imap.gmail.com')).toMatch(/app password/)
    expect(providerHint('a@yahoo.com', '')).toMatch(/app password/)
    expect(providerHint('a@outlook.com', '')).toMatch(/can't be brought over/)
    expect(providerHint('a@example.org', 'imap.example.org')).toBe('')
  })
})

describe('fileProblem', () => {
  it('accepts mail files, also without an extension', () => {
    for (const name of ['Inbox.mbox', 'message.eml', 'takeout-2026.zip', 'Inbox', 'Sent.MBOX']) {
      expect(fileProblem({ name, size: 10 })).toBeNull()
    }
  })

  it('refuses other files with a reason', () => {
    expect(fileProblem({ name: 'takeout.tgz', size: 10 })).toMatch(/\.zip format/)
    expect(fileProblem({ name: 'takeout.tar.gz', size: 10 })).toMatch(/\.zip format/)
    expect(fileProblem({ name: 'photo.jpg', size: 10 })).toMatch(/isn’t mail/)
    expect(fileProblem({ name: 'a.mbox', size: 0 })).toMatch(/empty/)
    expect(fileProblem({ name: 'a.zip', size: MAX_IMPORT_BYTES + 1 })).toMatch(/20 GB/)
  })
})

describe('uploadInParts', () => {
  // server keeps what arrived; parts may be cut by a broken connection.
  function server(size: number) {
    const state = { received: 0, calls: [] as [number, number][] }
    return {
      state,
      size,
      async send(offset: number, end: number, onBytes: (sent: number) => void, cutAfter?: number) {
        state.calls.push([offset, end])
        if (offset !== state.received) throw new ApiError(409, `the part must start at byte ${state.received}`)
        if (cutAfter !== undefined) {
          state.received = offset + cutAfter
          onBytes(cutAfter)
          throw new ApiError(0, "Can't reach the panel.")
        }
        onBytes(end - offset)
        state.received = end
        return end
      },
    }
  }

  it('sends a file in parts of the chosen size, never larger than the limit', async () => {
    const s = server(10 * MB)
    const progress: number[] = []
    await uploadInParts({
      size: s.size,
      partBytes: 4 * MB,
      sendPart: (offset, end, onBytes) => s.send(offset, end, onBytes),
      received: async () => s.state.received,
      onProgress: (done) => progress.push(done),
      wait: async () => {},
    })
    expect(s.state.calls).toEqual([
      [0, 4 * MB],
      [4 * MB, 8 * MB],
      [8 * MB, 10 * MB],
    ])
    expect(progress.at(-1)).toBe(10 * MB)
    expect(progress).toEqual([...progress].sort((a, b) => a - b))

    const big = server(600 * MB)
    await uploadInParts({
      size: big.size,
      partBytes: 1024 * MB,
      sendPart: (offset, end, onBytes) => big.send(offset, end, onBytes),
      received: async () => big.state.received,
      onProgress: () => {},
      wait: async () => {},
    })
    expect(big.state.calls.every(([from, to]) => to - from <= MAX_PART_BYTES)).toBe(true)
  })

  it('goes on from what the server has after the connection broke', async () => {
    const s = server(10 * MB)
    let broken = false
    const waits: number[] = []
    await uploadInParts({
      size: s.size,
      partBytes: 4 * MB,
      sendPart: (offset, end, onBytes) => {
        if (offset === 4 * MB && !broken) {
          broken = true
          return s.send(offset, end, onBytes, MB)
        }
        return s.send(offset, end, onBytes)
      },
      received: async () => s.state.received,
      onProgress: () => {},
      wait: async (ms) => {
        waits.push(ms)
      },
    })
    expect(s.state.calls).toEqual([
      [0, 4 * MB],
      [4 * MB, 8 * MB],
      [5 * MB, 9 * MB],
      [9 * MB, 10 * MB],
    ])
    expect(waits).toEqual([1000])
  })

  it('continues an upload the server has partly', async () => {
    const s = server(6 * MB)
    s.state.received = 2 * MB
    await uploadInParts({
      size: s.size,
      start: 2 * MB,
      partBytes: 4 * MB,
      sendPart: (offset, end, onBytes) => s.send(offset, end, onBytes),
      received: async () => s.state.received,
      onProgress: () => {},
      wait: async () => {},
    })
    expect(s.state.calls).toEqual([[2 * MB, 6 * MB]])
  })

  it('gives up after too many failures in a row', async () => {
    let tries = 0
    await expect(
      uploadInParts({
        size: MB,
        sendPart: async () => {
          tries++
          throw new ApiError(0, 'offline')
        },
        received: async () => 0,
        onProgress: () => {},
        maxRetries: 3,
        wait: async () => {},
      }),
    ).rejects.toThrow('offline')
    expect(tries).toBe(4)
  })

  it('stops at once on errors that a retry cannot fix', async () => {
    let tries = 0
    await expect(
      uploadInParts({
        size: MB,
        sendPart: async () => {
          tries++
          throw new ApiError(507, 'Not enough disk space')
        },
        received: async () => 0,
        onProgress: () => {},
        wait: async () => {},
      }),
    ).rejects.toThrow('disk space')
    expect(tries).toBe(1)
    expect(retriable(new ApiError(413, ''))).toBe(false)
    expect(retriable(new ApiError(502, ''))).toBe(true)
    expect(retriable(new Error('x'))).toBe(false)
  })

  it('stops when asked to', async () => {
    const controller = new AbortController()
    controller.abort()
    await expect(
      uploadInParts({
        size: MB,
        signal: controller.signal,
        sendPart: async () => MB,
        received: async () => 0,
        onProgress: () => {},
      }),
    ).rejects.toThrow('stopped')
  })
})

describe('folders', () => {
  const check: ImportCheck = {
    folders: [
      { remote: 'INBOX', local: 'INBOX', messages: 10, bytes: 10 * MB, selected: true },
      { remote: 'Sent', local: 'Sent', messages: 5, bytes: 2 * MB, selected: true },
      { remote: 'Spam', local: 'Junk', messages: 7, bytes: MB, selected: false, note: 'junk' },
      { remote: 'Bin', local: 'Trash', messages: 3, bytes: MB, selected: false, note: 'trash' },
      { remote: 'Deep', local: '', messages: 1, bytes: 1, selected: false, note: 'too-many' },
    ],
    messages: 15,
    bytes: 12 * MB,
    quota_bytes: 100 * MB,
    used_bytes: 90 * MB,
    quota_left: 10 * MB,
  }

  it('starts with what the server chose; for our own export also junk and deleted mail', () => {
    expect(initialSelection(check)).toEqual(['INBOX', 'Sent'])
    expect(initialSelection({ ...check, own_export: true })).toEqual(['INBOX', 'Sent', 'Spam', 'Bin'])
  })

  it('switches junk and deleted mail on and off, keeping the order', () => {
    const on = withKind(['INBOX', 'Sent'], check, 'junk', true)
    expect(on).toEqual(['INBOX', 'Sent', 'Spam'])
    expect(kindSelected(on, check, 'junk')).toBe(true)
    expect(kindSelected(on, check, 'trash')).toBe(false)
    expect(withKind(on, check, 'junk', false)).toEqual(['INBOX', 'Sent'])
    expect(withKind(['INBOX'], check, 'too-many', true)).toEqual(['INBOX'])
  })

  it('adds up the choice and compares it with the free space', () => {
    expect(selectionTotals(check, ['INBOX', 'Spam'])).toEqual({ messages: 17, bytes: 11 * MB })
    expect(fitsQuota(check, 10 * MB)).toBe(true)
    expect(fitsQuota(check, 11 * MB)).toBe(false)
    expect(fitsQuota({ ...check, quota_left: -1 }, 10 ** 12)).toBe(true)
    expect(quotaText(check, 11 * MB)).toMatch(/only 10 MB is free/)
    expect(quotaText({ ...check, quota_left: -1 }, MB)).toMatch(/no storage limit/)
  })

  it('says where a folder goes and why it is special', () => {
    expect(folderNote(check.folders[0])).toBe('')
    expect(folderNote(check.folders[2])).toBe('Into Junk · Junk mail')
    expect(folderNote(check.folders[4])).toMatch(/Too many folders/)
  })
})

describe('jobs', () => {
  const job = {
    kind: 'imap' as const,
    state: 'running',
    messages: 200,
    copied: 50,
    skipped: 30,
    failed: 20,
    bytes: 3 * MB,
    received: 0,
    file_size: 0,
  }

  it('knows which jobs still run and which need the password again', () => {
    expect(isActive(job)).toBe(true)
    expect(isActive({ state: 'paused' })).toBe(false)
    expect(needsPassword({ kind: 'imap', state: 'interrupted' })).toBe(true)
    expect(needsPassword({ kind: 'file', state: 'interrupted' })).toBe(false)
    expect(needsPassword({ kind: 'imap', state: 'done' })).toBe(false)
  })

  it('describes progress', () => {
    expect(jobShare(job)).toBe(50)
    expect(jobShare({ ...job, state: 'uploading', received: 25, file_size: 100 })).toBe(25)
    expect(jobShare({ ...job, messages: 0 })).toBeNull()
    expect(jobCounts(job)).toBe('50 messages copied (3 MB) · 30 already there · 20 not copied · of 200')
    expect(jobCounts({ ...job, state: 'done', copied: 0, skipped: 0, failed: 0, bytes: 0 })).toBe('No new mail to copy')
    expect(jobCounts({ ...job, state: 'running', copied: 0, skipped: 0, failed: 0, bytes: 0 })).toMatch(/^0 messages copied/)
    expect(jobState({ state: 'paused', kind: 'imap' }).sentence).toMatch(/per day/)
    expect(jobState({ state: 'interrupted', kind: 'file' }).sentence).toMatch(/by itself/)
  })
})
