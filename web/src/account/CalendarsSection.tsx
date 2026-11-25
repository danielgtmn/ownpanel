import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useCallback, useEffect, useRef, useState } from 'react'
import { copyText, Section } from '../shared/ui/index.ts'
import {
  accountErrorMessage,
  exportURL,
  importFile,
  isSessionEnded,
  loadCollections,
  type ClientSettings,
  type Collection,
  type Collections,
} from './api.ts'
import { calendarRows, entriesText, importedText, importFileError } from './rules.ts'

// Calendars and address books: the settings for calendar and contacts programs, and the upload and download of
// each calendar and address book.
export function CalendarsSection({
  client,
  address,
  appPasswordsOnly,
}: {
  client: ClientSettings
  address: string
  appPasswordsOnly: boolean
}) {
  const [data, setData] = useState<Collections | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState<string | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const target = useRef<Collection | null>(null)

  const reload = useCallback(
    () =>
      loadCollections().then(
        (next) => {
          setData(next)
          setError('')
        },
        (err: unknown) => {
          if (!isSessionEnded(err)) setError(accountErrorMessage(err))
        },
      ),
    [],
  )

  useEffect(() => {
    void reload()
  }, [reload])

  const rows = calendarRows(client, address, appPasswordsOnly)
  if (rows.length === 0) return null

  function choose(collection: Collection) {
    target.current = collection
    const input = fileInput.current
    if (!input) return
    input.accept = collection.kind === 'calendar' ? '.ics,text/calendar' : '.vcf,text/vcard'
    input.value = ''
    input.click()
  }

  async function upload(file: File) {
    const collection = target.current
    if (!collection || !data) return
    const problem = importFileError(file, collection.kind, data.max_file_bytes)
    if (problem) {
      toast.error(problem)
      return
    }
    setBusy(collection.kind + '/' + collection.name)
    try {
      const result = await importFile(collection, file)
      toast.success(`${collection.display_name}: ${importedText(result, collection.kind)}`)
    } catch (err) {
      if (!isSessionEnded(err)) toast.error(accountErrorMessage(err))
    }
    setBusy(null)
    await reload()
  }

  const calendars = data?.collections.filter((c) => c.kind === 'calendar') ?? []
  const books = data?.collections.filter((c) => c.kind === 'addressbook') ?? []

  return (
    <Section
      title="Calendars and contacts"
      description="Your calendars and address books stay in sync on your phones and computers. Add this account in a calendar or contacts program with these settings."
    >
      <dl className="account-client">
        {rows.map((row) => (
          <div key={row.label}>
            <dt>{row.label}</dt>
            <dd>
              {row.copy ? <code>{row.value}</code> : <span>{row.value}</span>}
              {row.copy ? (
                <Button className="copy-button" onClick={() => copyText(row.value)}>
                  Copy<span className="visually-hidden"> {row.label.toLowerCase()}</span>
                </Button>
              ) : null}
            </dd>
          </div>
        ))}
      </dl>
      <p className="muted account-calendars-note">
        On an iPhone, iPad or Mac, the setup for Apple devices above adds calendars and contacts as well. Most other
        programs only need your e-mail address and password and find the rest by themselves.
      </p>
      <input
        ref={fileInput}
        type="file"
        hidden
        aria-hidden="true"
        tabIndex={-1}
        onChange={(event) => {
          const file = event.target.files?.[0]
          if (file) void upload(file)
        }}
      />
      {error ? <p className="notice">{error}</p> : null}
      {data ? (
        <div className="account-collections">
          <CollectionList title="Calendars" items={calendars} busy={busy} onImport={choose} />
          <CollectionList title="Address books" items={books} busy={busy} onImport={choose} />
        </div>
      ) : error ? null : (
        <p className="muted">Loading your calendars and address books…</p>
      )}
    </Section>
  )
}

function CollectionList({
  title,
  items,
  busy,
  onImport,
}: {
  title: string
  items: Collection[]
  busy: string | null
  onImport: (collection: Collection) => void
}) {
  if (items.length === 0) return null
  return (
    <div className="account-collection-group">
      <h3 className="account-collection-title">{title}</h3>
      <ul aria-label={title}>
        {items.map((c) => {
          const key = c.kind + '/' + c.name
          const importing = busy === key
          return (
            <li className="account-app" key={key}>
              <div className="account-app-main">
                <strong className="account-collection-name">
                  {c.color ? <span className="account-swatch" style={{ background: c.color.slice(0, 7) }} aria-hidden="true" /> : null}
                  {c.display_name}
                </strong>
                <span className="muted">{entriesText(c)}</span>
              </div>
              <div className="account-collection-actions">
                <Button disabled={busy !== null} onClick={() => onImport(c)}>
                  {importing ? 'Importing…' : 'Import file'}
                  <span className="visually-hidden"> into {c.display_name}</span>
                </Button>
                <Button className="account-download" render={<a href={exportURL(c)} download />}>
                  Download<span className="visually-hidden"> {c.display_name}</span>
                </Button>
              </div>
            </li>
          )
        })}
      </ul>
    </div>
  )
}
