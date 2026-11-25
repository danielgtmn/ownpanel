import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@ui-sdk/react/select'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import type { SourceSite } from '../../shared/families/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'

type Props = {
  host: string | null
  sites: SourceSite[]
  onOpenChange: (open: boolean) => void
  onSubmit: (site: SourceSite, host: string) => Promise<void>
}

function AddToSiteForm({ host, sites, onSubmit }: { host: string; sites: SourceSite[]; onSubmit: Props['onSubmit'] }) {
  const [name, setName] = useState(sites[0]?.name ?? '')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const site = sites.find((candidate) => candidate.name === name)
    if (!site) {
      setError('Choose a website.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await onSubmit(site, host)
    } catch (failure) {
      setError(errorMessage(failure))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="site" invalid={error ? true : undefined}>
        <FieldLabel>Website</FieldLabel>
        <Select
          value={name}
          onValueChange={(value) => {
            if (value) setName(value)
            setError(null)
          }}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {sites.map((site) => (
              <SelectItem key={site.name} value={site.name}>
                {site.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FieldDescription>The website keeps its other addresses.</FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Add to website'}
        </Button>
      </div>
    </form>
  )
}

export function AddToSiteDialog({ host, sites, onOpenChange, onSubmit }: Props) {
  // Keeps the last address while the dialog closes, so its text doesn't jump.
  const [shown, setShown] = useState(host)
  if (host && host !== shown) setShown(host)
  return (
    <FormDialog
      open={host !== null}
      onOpenChange={onOpenChange}
      title="Add to a website"
      description={`Visitors of ${shown ?? ''} see the website you choose.`}
    >
      {shown ? <AddToSiteForm host={shown} sites={sites} onSubmit={onSubmit} /> : null}
    </FormDialog>
  )
}
