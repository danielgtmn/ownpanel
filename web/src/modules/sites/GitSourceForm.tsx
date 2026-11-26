import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import {
  inspectGitRepository,
  setGitSource,
  type GitRepositoryCheck,
  type SiteSource,
  type SiteSourceState,
} from './source.ts'

type Props = {
  site: string
  current: SiteSource | null
  publishingAvailable: boolean
  onLinked: (state: SiteSourceState) => Promise<void>
}

/**
 * Links a site to any Git repository by its address. Two steps: the address
 * is checked first (for SSH the panel shows the server key's fingerprint,
 * which the person compares with what their Git host publishes), then the
 * branch is chosen and the site linked with that fingerprint.
 */
export function GitSourceForm({ site, current, publishingAvailable, onLinked }: Props) {
  const git = current?.kind === 'git' ? current : null
  const [address, setAddress] = useState(git?.repository ?? '')
  const [checked, setChecked] = useState<GitRepositoryCheck | null>(null)
  const [branch, setBranch] = useState(git?.branch ?? 'main')
  const [publishOnPush, setPublishOnPush] = useState(git?.publish_on_push ?? true)
  const [errors, setErrors] = useState<{ address?: string; branch?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)

  async function inspect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!address.trim()) {
      setErrors({ address: 'Enter the repository’s address.' })
      return
    }
    setErrors({})
    setBusy(true)
    try {
      const result = await inspectGitRepository(site, address.trim())
      setChecked(result)
      setAddress(result.repository)
      if (result.branches && result.branches.length > 0 && !result.branches.includes(branch)) {
        setBranch(result.default_branch || result.branches[0])
      }
    } catch (error) {
      setErrors({ address: errorMessage(error) })
    } finally {
      setBusy(false)
    }
  }

  async function link(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!checked) return
    if (!branch.trim()) {
      setErrors({ branch: 'Enter the branch.' })
      return
    }
    setErrors({})
    setBusy(true)
    try {
      const state = await setGitSource(site, {
        repository: checked.repository,
        branch: branch.trim(),
        publish_on_push: publishOnPush,
        host_key: checked.host_key,
      })
      await onLinked(state)
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  if (!checked) {
    return (
      <form className="form" onSubmit={inspect} noValidate>
        <Field name="repository" invalid={errors.address ? true : undefined}>
          <FieldLabel>Repository address</FieldLabel>
          <Input
            value={address}
            onChange={(event) => setAddress(event.currentTarget.value)}
            placeholder="git@codeberg.org:owner/site.git"
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
          />
          <FieldDescription>
            The address you’d clone the repository with, like git@codeberg.org:owner/site.git. A public repository can
            also be given by its https:// address.
          </FieldDescription>
          {errors.address ? <FieldError match>{errors.address}</FieldError> : null}
        </Field>
        <div className="form-actions">
          <DialogClose disabled={busy}>Cancel</DialogClose>
          <Button type="submit" data-variant="solid" disabled={busy}>
            {busy ? 'Checking…' : 'Continue'}
          </Button>
        </div>
      </form>
    )
  }

  const ssh = checked.transport === 'ssh'
  const branches = checked.branches ?? []
  return (
    <form className="form" onSubmit={link} noValidate>
      <div className="git-check">
        <p className="git-check-address">{checked.repository}</p>
        {ssh ? (
          <>
            <p>
              {checked.pinned
                ? `This is the server key already trusted for ${checked.server}:`
                : `${checked.server} shows this server key:`}
            </p>
            <p className="git-fingerprint">
              <code>{checked.host_key}</code> <span className="muted">({keyTypeLabel(checked.host_key_type)})</span>
            </p>
            {checked.pinned ? null : (
              <p className="muted">
                Compare it with the fingerprint your Git host publishes (in its documentation or settings). Link only if
                they match: from then on the panel only talks to a server showing this key.
              </p>
            )}
          </>
        ) : (
          <p className="muted">A public repository: the panel reads it without a key.</p>
        )}
      </div>
      <Field name="branch" invalid={errors.branch ? true : undefined}>
        <FieldLabel>Branch</FieldLabel>
        {branches.length > 0 ? (
          <Select value={branch} onValueChange={(next) => next && setBranch(next)}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {branches.map((name) => (
                <SelectItem key={name} value={name}>
                  {name === checked.default_branch ? `${name} (default)` : name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : (
          <Input
            value={branch}
            onChange={(event) => setBranch(event.currentTarget.value)}
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
          />
        )}
        <FieldDescription>
          {ssh
            ? 'The panel can list the branches once its deploy key is added; type the name for now.'
            : 'The site’s code comes from this branch.'}
        </FieldDescription>
        {errors.branch ? <FieldError match>{errors.branch}</FieldError> : null}
      </Field>
      <Field className="switch-field">
        <Switch checked={publishOnPush} onCheckedChange={(next) => setPublishOnPush(next === true)}>
          <SwitchThumb />
        </Switch>
        <div className="choice-text">
          <FieldLabel>Publish on push</FieldLabel>
          <FieldDescription>
            {publishingAvailable
              ? 'Every push to the branch publishes the site, once your Git host reports pushes to the panel.'
              : 'Every push to the branch will publish the site. This starts once building on the server is available.'}
          </FieldDescription>
        </div>
      </Field>
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <Button disabled={busy} onClick={() => setChecked(null)}>
          Back
        </Button>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Linking…' : ssh && !checked.pinned ? 'The key matches, link' : current ? 'Save' : 'Link'}
        </Button>
      </div>
    </form>
  )
}

function keyTypeLabel(type?: string): string {
  switch (type) {
    case 'ssh-ed25519':
      return 'Ed25519'
    case 'ssh-rsa':
      return 'RSA'
    case undefined:
    case '':
      return 'key'
  }
  return type.startsWith('ecdsa-') ? 'ECDSA' : type
}
