import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'
import { startConnecting, validateOrganization } from './github.ts'

type Owner = 'personal' | 'organization'

function ConnectForm({ hasPersonal }: { hasPersonal: boolean }) {
  const [owner, setOwner] = useState<Owner>(hasPersonal ? 'organization' : 'personal')
  const [organization, setOrganization] = useState('')
  const [errors, setErrors] = useState<{ organization?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = owner === 'organization' ? organization.trim() : ''
    if (owner === 'organization') {
      const problem = validateOrganization(name)
      if (problem) {
        setErrors({ organization: problem })
        return
      }
    }
    setErrors({})
    setBusy(true)
    try {
      const { url } = await startConnecting(name)
      window.location.assign(url)
    } catch (error) {
      setErrors({ form: errorMessage(error) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field>
        <FieldLabel>Where are the repositories?</FieldLabel>
        <RadioGroup value={owner} onValueChange={(value) => value && setOwner(value as Owner)}>
          <Field>
            <Radio value="personal">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>My own GitHub account</FieldLabel>
              <FieldDescription>The account you're signed in with on GitHub.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="organization">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>An organisation</FieldLabel>
              <FieldDescription>You need to be one of its owners on GitHub.</FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>
      {owner === 'organization' ? (
        <Field name="organization" invalid={errors.organization ? true : undefined}>
          <FieldLabel>Organisation name</FieldLabel>
          <Input
            value={organization}
            autoComplete="off"
            spellCheck={false}
            placeholder="my-company"
            onValueChange={(value) => {
              setOrganization(value)
              setErrors({})
            }}
          />
          <FieldDescription>As in its address on GitHub, github.com/my-company.</FieldDescription>
          {errors.organization ? <FieldError match>{errors.organization}</FieldError> : null}
        </Field>
      ) : null}
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Opening GitHub…' : 'Continue to GitHub'}
        </Button>
      </div>
    </form>
  )
}

type Props = { open: boolean; onOpenChange: (open: boolean) => void; hasPersonal: boolean; another: boolean }

export function ConnectGitHubDialog({ open, onOpenChange, hasPersonal, another }: Props) {
  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={another ? 'Add another GitHub account' : 'Connect GitHub'}
      description="The panel creates its own app on GitHub for this account. You confirm it on GitHub, then choose which repositories the panel may see. Each account gets its own app."
    >
      <ConnectForm hasPersonal={hasPersonal} />
    </FormDialog>
  )
}
