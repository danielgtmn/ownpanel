import { Button } from '@ui-sdk/react/button'
import { Checkbox, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import {
  FileInput,
  FileInputDropzone,
  FileInputItem,
  FileInputList,
  FileInputTrigger,
  formatFileSize,
} from '@ui-sdk/react/file-input'
import { Input } from '@ui-sdk/react/input'
import { Progress, ProgressLabel, ProgressTrack, ProgressIndicator, ProgressValue } from '@ui-sdk/react/progress'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { toast } from '@ui-sdk/react/toast'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog, PasswordField, UploadIcon } from '../../shared/ui/index.ts'
import { cancelImport, checkImport, createUpload, getImport, startFileImport, startImport, uploadPart } from './api.ts'
import { formatBytes } from './rules.ts'
import {
  canImport,
  choicesFor,
  fileProblem,
  folderNote,
  hasErrors,
  initialSelection,
  kindSelected,
  normaliseHost,
  providerHint,
  quotaText,
  SECURE_PORT,
  selectionTotals,
  suggestServer,
  UPGRADE_PORT,
  uploadInParts,
  validateSource,
  withKind,
  type SourceErrors,
} from './transfer.ts'
import type { ImportCheck, MailImport, Mailbox } from './types.ts'

type Target = Pick<Mailbox, 'domain' | 'local_part' | 'address'>
type Way = 'account' | 'file'

// ---- choosing folders (both ways end here) ----

function FolderChoice({
  check,
  busy,
  error,
  startLabel,
  onBack,
  onStart,
}: {
  check: ImportCheck
  busy: boolean
  error: string | null
  startLabel: string
  onBack: (() => void) | null
  onStart: (folders: string[]) => void
}) {
  const [selected, setSelected] = useState(() => initialSelection(check))
  const totals = selectionTotals(check, selected)
  const has = (note: string) => check.folders.some((f) => f.note === note && canImport(f))
  const toggle = (name: string, on: boolean) =>
    setSelected((current) =>
      on ? check.folders.filter((f) => current.includes(f.remote) || f.remote === name).map((f) => f.remote) : current.filter((n) => n !== name),
    )

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (selected.length > 0) onStart(selected)
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      {check.folders.length === 0 ? (
        <p className="notice">No mail was found to copy.</p>
      ) : (
        <fieldset className="import-folders">
          <legend>Folders to copy</legend>
          <ul>
            {check.folders.map((folder) => (
              <li key={folder.remote}>
                <label data-slot="label">
                  <Checkbox
                    checked={selected.includes(folder.remote)}
                    disabled={!canImport(folder) || busy}
                    onCheckedChange={(next) => toggle(folder.remote, next === true)}
                  >
                    <CheckboxIndicator />
                  </Checkbox>
                  <span className="import-folder-name">{folder.remote}</span>
                  <span className="muted import-folder-size">
                    {folder.messages === 1 ? '1 message' : `${folder.messages.toLocaleString('en')} messages`} ·{' '}
                    {formatBytes(folder.bytes)}
                  </span>
                </label>
                {folderNote(folder) ? <p className="muted import-folder-note">{folderNote(folder)}</p> : null}
              </li>
            ))}
          </ul>
        </fieldset>
      )}
      {has('junk') ? (
        <Field className="import-switch">
          <Switch
            checked={kindSelected(selected, check, 'junk')}
            disabled={busy}
            onCheckedChange={(next) => setSelected((current) => withKind(current, check, 'junk', next === true))}
          >
            <SwitchThumb />
          </Switch>
          <div className="choice-text">
            <FieldLabel>Include junk mail</FieldLabel>
            <FieldDescription>Mail the other account sorted out as spam.</FieldDescription>
          </div>
        </Field>
      ) : null}
      {has('trash') ? (
        <Field className="import-switch">
          <Switch
            checked={kindSelected(selected, check, 'trash')}
            disabled={busy}
            onCheckedChange={(next) => setSelected((current) => withKind(current, check, 'trash', next === true))}
          >
            <SwitchThumb />
          </Switch>
          <div className="choice-text">
            <FieldLabel>Include deleted mail</FieldLabel>
            <FieldDescription>Mail in the other account's trash.</FieldDescription>
          </div>
        </Field>
      ) : null}
      <p className="import-total">
        <strong>{totals.messages.toLocaleString('en')} messages</strong> · {quotaText(check, totals.bytes)}
      </p>
      {check.skipped_entries ? (
        <p className="muted import-note">
          {check.skipped_entries === 1
            ? '1 item in the file isn’t mail and is left out.'
            : `${check.skipped_entries} items in the file aren’t mail and are left out.`}
        </p>
      ) : null}
      <p className="muted import-note">Messages that are in the mailbox already are skipped, so you can run the same import again later.</p>
      {error ? (
        <p className="form-error" role="alert">
          {error}
        </p>
      ) : null}
      <div className="form-actions">
        {onBack ? (
          <Button disabled={busy} onClick={onBack}>
            Back
          </Button>
        ) : (
          <DialogClose disabled={busy}>Cancel</DialogClose>
        )}
        <Button type="submit" data-variant="solid" disabled={busy || selected.length === 0}>
          {busy ? 'Starting…' : startLabel}
        </Button>
      </div>
    </form>
  )
}

// ---- another mail account ----

function AccountForm({ target, onStarted }: { target: Target; onStarted: (job: MailImport) => void }) {
  const [host, setHost] = useState('')
  const [port, setPort] = useState(SECURE_PORT)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<SourceErrors & { form?: string }>({})
  const [busy, setBusy] = useState(false)
  const [check, setCheck] = useState<ImportCheck | null>(null)
  const hint = providerHint(username, host)
  const source = () => ({ host: normaliseHost(host), port, username: username.trim(), password })

  async function runCheck(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateSource({ host, port, username, password })
    setErrors(found)
    if (hasErrors(found)) return
    setBusy(true)
    try {
      setCheck(await checkImport(target.domain, target.local_part, source()))
    } catch (failure) {
      setErrors({ form: errorMessage(failure) })
    } finally {
      setBusy(false)
    }
  }

  async function start(folders: string[]) {
    setBusy(true)
    setErrors({})
    try {
      onStarted(await startImport(target.domain, target.local_part, source(), choicesFor(folders)))
    } catch (failure) {
      setErrors({ form: errorMessage(failure) })
      setBusy(false)
    }
  }

  if (check) {
    return (
      <FolderChoice
        check={check}
        busy={busy}
        error={errors.form ?? null}
        startLabel="Start copying"
        onBack={() => {
          setCheck(null)
          setErrors({})
        }}
        onStart={start}
      />
    )
  }

  const clear = (key: keyof SourceErrors) => setErrors((current) => ({ ...current, [key]: undefined, form: undefined }))

  return (
    <form className="form" onSubmit={runCheck} noValidate>
      <Field name="username" invalid={errors.username ? true : undefined}>
        <FieldLabel>User name</FieldLabel>
        <Input
          value={username}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder="you@example.org"
          onValueChange={(value) => {
            setUsername(value)
            clear('username')
          }}
          onBlur={() => {
            if (!host.trim()) setHost(suggestServer(username))
          }}
        />
        <FieldDescription>Usually the full e-mail address of the other account.</FieldDescription>
        {errors.username ? <FieldError match>{errors.username}</FieldError> : null}
      </Field>
      <PasswordField
        name="password"
        label="Password"
        value={password}
        autoComplete="current-password"
        description="Used only while mail is copied; it isn't saved."
        error={errors.password}
        onValueChange={(value) => {
          setPassword(value)
          clear('password')
        }}
      />
      {hint ? <p className="notice">{hint}</p> : null}
      <Field name="host" invalid={errors.host ? true : undefined}>
        <FieldLabel>Server</FieldLabel>
        <Input
          value={host}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder="imap.example.org"
          onValueChange={(value) => {
            setHost(value)
            clear('host')
          }}
        />
        <FieldDescription>The incoming mail server your provider names for mail programs.</FieldDescription>
        {errors.host ? <FieldError match>{errors.host}</FieldError> : null}
      </Field>
      <Field>
        <FieldLabel>Connection</FieldLabel>
        <RadioGroup value={String(port)} onValueChange={(value) => value && setPort(Number(value))}>
          <Field>
            <Radio value={String(SECURE_PORT)}>
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Encrypted from the start (port 993)</FieldLabel>
              <FieldDescription>What almost every provider offers.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value={String(UPGRADE_PORT)}>
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Encrypted after connecting (port 143)</FieldLabel>
              <FieldDescription>Only if the provider says so. Signing in still happens encrypted.</FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Signing in…' : 'Check the account'}
        </Button>
      </div>
    </form>
  )
}

// ---- a file ----

const wait = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms))

// waitUntilRead polls a job until its file was read.
async function waitUntilRead(target: Target, id: number, signal: AbortSignal): Promise<MailImport> {
  for (;;) {
    const job = await getImport(target.domain, target.local_part, id)
    if (job.state !== 'uploading' && job.state !== 'checking') return job
    if (signal.aborted) throw new DOMException('Stopped.', 'AbortError')
    await wait(1500)
  }
}

function FileForm({
  target,
  ready,
  onStarted,
  onBusyChange,
}: {
  target: Target
  ready: MailImport | null
  onStarted: (job: MailImport) => void
  onBusyChange: (busy: boolean) => void
}) {
  const [files, setFiles] = useState<File[]>([])
  const [problem, setProblem] = useState<string | null>(null)
  const [phase, setPhase] = useState<'pick' | 'upload' | 'read' | 'choose'>(ready ? 'choose' : 'pick')
  const [sent, setSent] = useState(0)
  const [job, setJob] = useState<MailImport | null>(ready)
  const [busy, setBusy] = useState(false)
  const abort = useRef<AbortController | null>(null)
  const jobRef = useRef<MailImport | null>(ready)

  // Closing the dialog during the upload stops it and drops the half file.
  useEffect(() => {
    return () => {
      const running = abort.current
      const current = jobRef.current
      if (running && !running.signal.aborted) {
        running.abort()
        if (current && current.state === 'uploading') void cancelImport(target.domain, target.local_part, current.id).catch(() => {})
      }
    }
  }, [target.domain, target.local_part])

  const file = files[0]

  async function upload() {
    if (!file) return
    const why = fileProblem(file)
    if (why) {
      setProblem(why)
      return
    }
    setProblem(null)
    const controller = new AbortController()
    abort.current = controller
    onBusyChange(true)
    setPhase('upload')
    try {
      const created = await createUpload(target.domain, target.local_part, file.name, file.size)
      jobRef.current = created
      await uploadInParts({
        size: file.size,
        start: created.received ?? 0,
        signal: controller.signal,
        sendPart: (offset, end, onBytes) =>
          uploadPart(target.domain, target.local_part, created.id, offset, file.slice(offset, end), onBytes, controller.signal),
        received: async () => (await getImport(target.domain, target.local_part, created.id)).received ?? 0,
        onProgress: setSent,
      })
      setPhase('read')
      const read = await waitUntilRead(target, created.id, controller.signal)
      jobRef.current = read
      if (read.state !== 'ready' || !read.check) {
        throw new Error(read.error || 'The file could not be read.')
      }
      setJob(read)
      setPhase('choose')
    } catch (failure) {
      if (controller.signal.aborted) return
      setProblem(failure instanceof Error && !('status' in failure) ? failure.message : errorMessage(failure))
      setPhase('pick')
    } finally {
      abort.current = null
      onBusyChange(false)
    }
  }

  async function start(folders: string[]) {
    if (!job) return
    setBusy(true)
    setProblem(null)
    try {
      onStarted(await startFileImport(target.domain, target.local_part, job.id, choicesFor(folders)))
    } catch (failure) {
      setProblem(errorMessage(failure))
      setBusy(false)
    }
  }

  if (phase === 'choose' && job?.check) {
    return <FolderChoice check={job.check} busy={busy} error={problem} startLabel="Start copying" onBack={null} onStart={start} />
  }

  if (phase === 'upload' || phase === 'read') {
    const size = file?.size ?? 1
    const share = phase === 'read' ? 100 : Math.floor((sent / size) * 100)
    return (
      <div className="form">
        <Progress value={share}>
          <ProgressLabel>{phase === 'read' ? 'Reading the file…' : `Uploading ${file?.name ?? ''}`}</ProgressLabel>
          <ProgressValue />
          <ProgressTrack>
            <ProgressIndicator />
          </ProgressTrack>
        </Progress>
        <p className="muted">
          {phase === 'read'
            ? 'Looking at what the file holds. Nothing is copied yet.'
            : `${formatFileSize(sent)} of ${formatFileSize(size)}. If the connection breaks, the upload goes on where it stopped. Keep this window open.`}
        </p>
        <div className="form-actions">
          <DialogClose>Stop</DialogClose>
        </div>
      </div>
    )
  }

  return (
    <div className="form">
      <FileInput
        value={files}
        onValueChange={(next) => {
          setFiles(next)
          setProblem(next[0] ? fileProblem(next[0]) : null)
        }}
      >
        <FileInputDropzone>
          <UploadIcon />
          <p>Drop the file here</p>
          <p className="muted">.mbox, .eml or .zip, up to 20 GB</p>
          <FileInputTrigger>{file ? 'Choose a different file' : 'Choose a file'}</FileInputTrigger>
        </FileInputDropzone>
        <FileInputList aria-label="Selected file">
          <FileInputItem>
            {({ file: item }) => (
              <>
                <span>{item.name}</span>
                <span>{formatFileSize(item.size)}</span>
              </>
            )}
          </FileInputItem>
        </FileInputList>
      </FileInput>
      <ul className="muted import-hints">
        <li>Google: export your mail with Google Takeout and choose the .zip format.</li>
        <li>Thunderbird: export a folder as .mbox, or put the profile's mail folders into a .zip.</li>
        <li>Apple Mail: Mailbox › Export Mailbox, then put the folder into a .zip.</li>
        <li>A file from “Download all mail” of this panel brings back every folder as it was.</li>
      </ul>
      {problem ? (
        <p className="form-error" role="alert">
          {problem}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button data-variant="solid" disabled={!file || fileProblem(file) !== null} onClick={upload}>
          Upload and check
        </Button>
      </div>
    </div>
  )
}

// ---- the dialog ----

export function ImportDialog({
  target,
  ready,
  open,
  onOpenChange,
  onStarted,
}: {
  target: Target | null
  // A file job that was read already: go straight to choosing folders.
  ready?: MailImport | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onStarted: () => void
}) {
  const [way, setWay] = useState<Way>('account')
  const [uploading, setUploading] = useState(false)
  if (!target) return null

  function started(job: MailImport) {
    toast.success(`Copying mail into ${target?.address ?? job.address} started. You can close this page; it goes on.`)
    onOpenChange(false)
    onStarted()
  }

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Import mail into ${target.address}`}
      description="Copies mail from another account or a file into this mailbox. Mail that is there already stays."
    >
      {ready ? (
        <FileForm target={target} ready={ready} onStarted={started} onBusyChange={setUploading} />
      ) : (
        <div className="import-dialog">
          {!uploading ? (
            <RadioGroup className="import-ways" value={way} onValueChange={(value) => value && setWay(value as Way)}>
              <Field>
                <Radio value="account">
                  <RadioIndicator />
                </Radio>
                <div className="choice-text">
                  <FieldLabel>From another mail account</FieldLabel>
                  <FieldDescription>Signs in to your old provider and copies the folders you choose.</FieldDescription>
                </div>
              </Field>
              <Field>
                <Radio value="file">
                  <RadioIndicator />
                </Radio>
                <div className="choice-text">
                  <FieldLabel>From a file</FieldLabel>
                  <FieldDescription>Mail you exported from a mail program or provider.</FieldDescription>
                </div>
              </Field>
            </RadioGroup>
          ) : null}
          {way === 'account' ? (
            <AccountForm target={target} onStarted={started} />
          ) : (
            <FileForm target={target} ready={null} onStarted={started} onBusyChange={setUploading} />
          )}
        </div>
      )}
    </FormDialog>
  )
}
