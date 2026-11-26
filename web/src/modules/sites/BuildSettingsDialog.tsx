import { Button } from '@ui-sdk/react/button'
import { Checkbox, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { Textarea } from '@ui-sdk/react/textarea'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog, LoadingState } from '../../shared/ui/index.ts'
import {
  checkVariables,
  confidenceLabel,
  environmentLabel,
  proposeBuildSettings,
  saveBuildSettings,
  variableRows,
  variablesInput,
  type BuildProposal,
  type BuildSettings,
  type VariableRow,
} from './builds.ts'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  site: string
  settings: BuildSettings
  onSaved: (settings: BuildSettings) => Promise<void>
}

type Proposal =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ready'; data: BuildProposal }
  | { status: 'error'; message: string }

function ProposalNote({ proposal, onUse }: { proposal: Proposal; onUse: (p: BuildProposal) => void }) {
  if (proposal.status === 'idle') return null
  if (proposal.status === 'loading') return <LoadingState label="Looking at the repository…" />
  if (proposal.status === 'error') {
    return (
      <p className="muted" role="status">
        Couldn't look at the repository: {proposal.message}
      </p>
    )
  }
  const p = proposal.data
  return (
    <div className="build-proposal" role="group" aria-label="Suggestion from the repository">
      <p className="build-proposal-title">
        {p.preset ? `${confidenceLabel(p.confidence)}: ${p.name}` : 'The kind of site was not recognised'}
        <span className="muted">
          {' '}
          · branch {p.branch}, commit <code>{p.commit.slice(0, 7)}</code>
        </span>
      </p>
      {p.notes?.length ? (
        <ul className="build-proposal-notes">
          {p.notes.map((note) => (
            <li key={note}>{note}</li>
          ))}
        </ul>
      ) : null}
      {p.also?.length ? <p className="muted">Also found: {p.also.join(', ')}.</p> : null}
      {p.preset ? (
        <Button onClick={() => onUse(p)}>
          Use the suggestion<span className="visually-hidden"> for the commands and the folder</span>
        </Button>
      ) : null}
    </div>
  )
}

function SettingsForm({ site, settings, onSaved }: Omit<Props, 'open' | 'onOpenChange'>) {
  const [command, setCommand] = useState(settings.command)
  const [output, setOutput] = useState(settings.configured ? settings.output : 'dist')
  const [image, setImage] = useState(settings.image || settings.images[0] || '')
  const [rows, setRows] = useState<VariableRow[]>(() => variableRows(settings))
  const nextKey = useRef(rows.length)
  const [proposal, setProposal] = useState<Proposal>({ status: 'idle' })
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  function applyProposal(p: BuildProposal) {
    setCommand(p.settings.command)
    setOutput(p.settings.output)
    if (p.settings.image && settings.images.includes(p.settings.image)) setImage(p.settings.image)
    setError(null)
  }

  async function suggest(fill: boolean) {
    setProposal({ status: 'loading' })
    try {
      const data = await proposeBuildSettings(site)
      setProposal({ status: 'ready', data })
      if (fill && data.preset) applyProposal(data)
    } catch (e) {
      setProposal({ status: 'error', message: errorMessage(e) })
    }
  }

  // A site without settings starts from what its repository suggests.
  const asked = useRef(false)
  useEffect(() => {
    if (settings.configured || asked.current) return
    asked.current = true
    void suggest(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function change(key: number, patch: Partial<VariableRow>) {
    setRows((list) => list.map((row) => (row.key === key ? { ...row, ...patch } : row)))
    setError(null)
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!output.trim()) {
      setError('Enter the folder to publish, or . for the whole repository.')
      return
    }
    const problem = checkVariables(rows)
    if (problem) {
      setError(problem)
      return
    }
    setBusy(true)
    setError(null)
    try {
      const saved = await saveBuildSettings(site, {
        command,
        output: output.trim(),
        image: command.trim() ? image : '',
        variables: variablesInput(rows),
      })
      await onSaved(saved)
    } catch (e) {
      setError(errorMessage(e))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <ProposalNote proposal={proposal} onUse={applyProposal} />
      <Field name="command">
        <FieldLabel>Commands</FieldLabel>
        <Textarea
          value={command}
          rows={3}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder={'npm ci\nnpm run build'}
          onValueChange={(value) => {
            setCommand(value)
            setError(null)
          }}
        />
        <FieldDescription>
          Run one after the other in the repository’s files; the build stops at the first that fails. Leave empty to
          publish the folder as it is.
        </FieldDescription>
      </Field>
      <Field name="output">
        <FieldLabel>Folder to publish</FieldLabel>
        <Input
          value={output}
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder="dist"
          onValueChange={(value) => {
            setOutput(value)
            setError(null)
          }}
        />
        <FieldDescription>Where the commands leave the finished site, inside the repository. . is all of it.</FieldDescription>
      </Field>
      {command.trim() && settings.images.length > 0 ? (
        <Field name="image">
          <FieldLabel>Environment</FieldLabel>
          <Select value={image} onValueChange={(next) => next && setImage(next)}>
            <SelectTrigger>
              <SelectValue>{environmentLabel(image)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {settings.images.map((name) => (
                <SelectItem key={name} value={name}>
                  {environmentLabel(name)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <FieldDescription>The tools the commands run with.</FieldDescription>
        </Field>
      ) : null}
      <fieldset className="build-variables">
        <legend>Variables</legend>
        <p className="muted">
          Values the build can read, like an address of a service. Secret values are kept encrypted, never shown again
          and hidden in the output.
        </p>
        {rows.map((row) => (
          <div key={row.key} className="build-variable" role="group" aria-label={row.name || 'New variable'}>
            <Field name={`name-${row.key}`}>
              <FieldLabel>Name</FieldLabel>
              <Input
                value={row.name}
                autoComplete="off"
                autoCapitalize="characters"
                spellCheck={false}
                placeholder="API_URL"
                onValueChange={(value) => change(row.key, { name: value.toUpperCase() })}
              />
            </Field>
            <Field name={`value-${row.key}`}>
              <FieldLabel>Value</FieldLabel>
              <Input
                value={row.value}
                type={row.secret ? 'password' : 'text'}
                autoComplete="off"
                spellCheck={false}
                placeholder={row.stored && row.secret ? 'Unchanged' : ''}
                onValueChange={(value) => change(row.key, { value })}
              />
            </Field>
            <Field className="build-variable-secret">
              <Checkbox
                checked={row.secret}
                disabled={row.stored && row.secret}
                onCheckedChange={(next) => change(row.key, { secret: next === true })}
              >
                <CheckboxIndicator />
              </Checkbox>
              <FieldLabel>Secret</FieldLabel>
            </Field>
            <Button
              onClick={() => {
                setRows((list) => list.filter((r) => r.key !== row.key))
                setError(null)
              }}
            >
              Remove<span className="visually-hidden"> {row.name || 'the new variable'}</span>
            </Button>
          </div>
        ))}
        <Button
          onClick={() => {
            const key = nextKey.current++
            setRows((list) => [...list, { key, name: '', value: '', secret: false, stored: false }])
          }}
        >
          Add variable
        </Button>
      </fieldset>
      {error ? (
        <p className="form-error" role="alert">
          {error}
        </p>
      ) : null}
      <div className="form-actions">
        {proposal.status !== 'loading' ? (
          <Button className="form-actions-start" onClick={() => void suggest(false)}>
            Suggest from the repository
          </Button>
        ) : null}
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy} pendingAnnouncement="Saving">
          Save
        </Button>
      </div>
    </form>
  )
}

export function BuildSettingsDialog({ open, onOpenChange, site, settings, onSaved }: Props) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Build settings"
      description={`How ${site} is built from its repository before it is published.`}
    >
      <SettingsForm site={site} settings={settings} onSaved={onSaved} />
    </FormDialog>
  )
}
