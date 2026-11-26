import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { hrefFor } from '../../shared/routing/index.ts'
import { FormDialog, LoadingState } from '../../shared/ui/index.ts'
import { GitSourceForm } from './GitSourceForm.tsx'
import {
  branchChoices,
  initialBranch,
  listLinkableRepositories,
  listSourceBranches,
  repositoryKey,
  setSiteSource,
  type LinkableRepository,
  type SiteSource,
  type SiteSourceState,
} from './source.ts'

type FormProps = {
  site: string
  current: SiteSource | null
  publishingAvailable: boolean
  onLinked: (state: SiteSourceState) => Promise<void>
}

function BranchField({
  site,
  repository,
  current,
  value,
  onChange,
  error,
}: {
  site: string
  repository: LinkableRepository
  current?: string
  value: string
  onChange: (branch: string) => void
  error?: string
}) {
  const branches = useResource(() => listSourceBranches(site, repository), `branches:${site}:${repositoryKey(repository)}`)
  const loaded = branches.status === 'ready' ? branches.data : null
  const choices = loaded ? branchChoices(loaded, current).filter((name) => loaded.branches.includes(name)) : []
  const selected = loaded ? (value && choices.includes(value) ? value : initialBranch(loaded, current)) : value
  // The preselected branch becomes the form's value once the list is there.
  useEffect(() => {
    if (selected !== value) onChange(selected)
  }, [selected, value, onChange])

  if (branches.status === 'loading') return <LoadingState label="Loading branches…" />
  if (branches.status === 'error') {
    return (
      <p className="form-error" role="alert">
        Couldn't load the branches: {errorMessage(branches.error)}
      </p>
    )
  }
  return (
    <Field name="branch" invalid={error ? true : undefined}>
      <FieldLabel>Branch</FieldLabel>
      {choices.length === 0 ? (
        <p className="muted">This repository has no branches yet. Push one first.</p>
      ) : (
        <Select value={selected} onValueChange={(next) => next && onChange(next)}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {choices.map((name) => (
              <SelectItem key={name} value={name}>
                {name === branches.data.default_branch ? `${name} (default)` : name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      <FieldDescription>
        {branches.data.truncated
          ? 'The repository has more branches than shown here.'
          : 'The site’s code comes from this branch.'}
      </FieldDescription>
      {error ? <FieldError match>{error}</FieldError> : null}
    </Field>
  )
}

function SourceForm({ site, current, publishingAvailable, onLinked }: FormProps) {
  const repositories = useResource(() => listLinkableRepositories(site), `linkable:${site}`)
  const [chosen, setChosen] = useState(current ? repositoryKey(current) : '')
  const [branch, setBranch] = useState(current?.branch ?? '')
  const [publishOnPush, setPublishOnPush] = useState(current?.publish_on_push ?? true)
  const [errors, setErrors] = useState<{ repository?: string; branch?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)
  const chooseBranch = useCallback((next: string) => {
    setBranch(next)
    setErrors((e) => ({ ...e, branch: undefined, form: undefined }))
  }, [])

  if (repositories.status === 'loading') return <LoadingState label="Loading repositories…" />
  if (repositories.status === 'error') {
    return (
      <>
        <p className="form-error" role="alert">
          Couldn't load the repositories: {errorMessage(repositories.error)}
        </p>
        <div className="form-actions">
          <DialogClose>Close</DialogClose>
        </div>
      </>
    )
  }
  const list = repositories.data
  const repository = list.repositories.find((r) => repositoryKey(r) === chosen) ?? null

  if (list.repositories.length === 0) {
    return (
      <>
        <p>
          {list.connected
            ? 'The connected GitHub accounts don’t share any repositories with the panel yet. Choose them in the settings.'
            : 'No GitHub account is connected yet. Connect one in the settings first.'}
        </p>
        {list.problems.map((problem) => (
          <p key={problem} className="form-error">
            {problem}
          </p>
        ))}
        <div className="form-actions">
          <DialogClose>Cancel</DialogClose>
          <Button data-variant="solid" render={<a href={hrefFor('settings')} />}>
            Open the settings
          </Button>
        </div>
      </>
    )
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!repository) {
      setErrors({ repository: 'Choose a repository.' })
      return
    }
    if (!branch) {
      setErrors({ branch: 'Choose a branch.' })
      return
    }
    setErrors({})
    setBusy(true)
    try {
      const state = await setSiteSource(site, {
        installation_id: repository.installation_id,
        repository_id: repository.id,
        branch,
        publish_on_push: publishOnPush,
      })
      await onLinked(state)
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="repository" invalid={errors.repository ? true : undefined}>
        <FieldLabel>Repository</FieldLabel>
        <Select
          value={chosen || null}
          onValueChange={(next) => {
            if (!next) return
            setChosen(next)
            setBranch('')
            setErrors({})
          }}
        >
          <SelectTrigger>
            <SelectValue placeholder="Choose a repository" />
          </SelectTrigger>
          <SelectContent>
            {list.repositories.map((r) => (
              <SelectItem key={repositoryKey(r)} value={repositoryKey(r)}>
                {r.archived ? `${r.full_name} (archived)` : r.full_name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FieldDescription>
          {list.truncated
            ? 'Not every repository is listed: there are more than the panel shows.'
            : 'Repositories the connected GitHub accounts share with the panel.'}
        </FieldDescription>
        {errors.repository ? <FieldError match>{errors.repository}</FieldError> : null}
      </Field>
      {list.problems.map((problem) => (
        <p key={problem} className="muted">
          {problem}
        </p>
      ))}
      {repository ? (
        <BranchField
          key={chosen}
          site={site}
          repository={repository}
          current={current && repositoryKey(current) === chosen ? current.branch : undefined}
          value={branch}
          onChange={chooseBranch}
          error={errors.branch}
        />
      ) : null}
      <Field className="switch-field">
        <Switch checked={publishOnPush} onCheckedChange={(next) => setPublishOnPush(next === true)}>
          <SwitchThumb />
        </Switch>
        <div className="choice-text">
          <FieldLabel>Publish on push</FieldLabel>
          <FieldDescription>
            {publishingAvailable
              ? 'Every push to the branch publishes the site.'
              : 'Every push to the branch will publish the site. This starts once building on the server is available; until then, nothing is published automatically.'}
          </FieldDescription>
        </div>
      </Field>
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Checking…' : current ? 'Save' : 'Link'}
        </Button>
      </div>
    </form>
  )
}

type Props = FormProps & {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Administrators may also choose a repository of the connected GitHub accounts. */
  canUseGitHub: boolean
}

type Origin = 'github' | 'git'

function SourceChoice({ canUseGitHub, ...form }: FormProps & { canUseGitHub: boolean }) {
  const [origin, setOrigin] = useState<Origin>(canUseGitHub ? (form.current?.kind ?? 'github') : 'git')
  return (
    <>
      {canUseGitHub ? (
        <Field>
          <FieldLabel>Where is the repository?</FieldLabel>
          <RadioGroup value={origin} onValueChange={(value) => value && setOrigin(value as Origin)}>
            <Field>
              <Radio value="github">
                <RadioIndicator />
              </Radio>
              <div className="choice-text">
                <FieldLabel>GitHub</FieldLabel>
                <FieldDescription>A repository of a GitHub account connected in the settings.</FieldDescription>
              </div>
            </Field>
            <Field>
              <Radio value="git">
                <RadioIndicator />
              </Radio>
              <div className="choice-text">
                <FieldLabel>Other Git host</FieldLabel>
                <FieldDescription>
                  Any repository by its address: GitLab, Codeberg, Forgejo, Gitea, Bitbucket or GitHub without a
                  connection. The panel makes a key for this site that you add to the repository.
                </FieldDescription>
              </div>
            </Field>
          </RadioGroup>
        </Field>
      ) : null}
      {origin === 'github' ? <SourceForm key="github" {...form} /> : <GitSourceForm key="git" {...form} />}
    </>
  )
}

export function SourceDialog({ open, onOpenChange, canUseGitHub, ...form }: Props) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={form.current ? 'Change the source' : 'Link a repository'}
      description="Choose the repository and branch this site's code comes from."
    >
      <SourceChoice canUseGitHub={canUseGitHub} {...form} />
    </FormDialog>
  )
}
