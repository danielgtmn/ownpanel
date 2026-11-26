import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import { setSitePreviews, type SiteSource, type SiteSourceState } from './source.ts'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  site: string
  source: SiteSource
  onSaved: (state: SiteSourceState) => Promise<void>
}

function PreviewsForm({ site, source, onSaved }: Omit<Props, 'open' | 'onOpenChange'>) {
  const [pullRequests, setPullRequests] = useState(source.pull_request_previews)
  const [forks, setForks] = useState(source.fork_previews)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError(null)
    setBusy(true)
    try {
      await onSaved(await setSitePreviews(site, { pull_requests: pullRequests, forks: pullRequests && forks }))
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field className="switch-field">
        <Switch checked={pullRequests} onCheckedChange={(next) => setPullRequests(next === true)}>
          <SwitchThumb />
        </Switch>
        <div className="choice-text">
          <FieldLabel>Previews of pull requests</FieldLabel>
          <FieldDescription>
            Every pull request against {source.branch} is built as a version that is not live, with its own preview
            address. The address is posted on the pull request and updated with every push; the preview is removed
            when the pull request is closed.
          </FieldDescription>
        </div>
      </Field>
      <Field className="switch-field">
        <Switch
          checked={pullRequests && forks}
          disabled={!pullRequests}
          onCheckedChange={(next) => setForks(next === true)}
        >
          <SwitchThumb />
        </Switch>
        <div className="choice-text">
          <FieldLabel>Also from forks</FieldLabel>
          <FieldDescription>
            Pull requests from copies of the repository run code from people outside it. They are built without the
            site's variables and secrets, and only when this is on.
          </FieldDescription>
        </div>
      </Field>
      {error ? (
        <p className="form-error" role="alert">
          {error}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

/** Which pull requests are built as previews. */
export function PreviewsDialog({ open, onOpenChange, site, source, onSaved }: Props) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Previews of pull requests"
      description={`Which pull requests to ${source.repository} are built as previews of ${site}.`}
    >
      <PreviewsForm site={site} source={source} onSaved={onSaved} />
    </FormDialog>
  )
}
