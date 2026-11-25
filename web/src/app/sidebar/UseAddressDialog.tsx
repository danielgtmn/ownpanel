import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input, InputGroup } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { useState, type FormEvent } from 'react'
import { subdomainHost, type Address, type AddressAction } from '../../shared/families/index.ts'
import { FormDialog } from '../../shared/ui/index.ts'

/** Which address the dialog is about: one that exists, or a new subdomain typed in the dialog. */
export type UseTarget = { kind: 'existing'; host: string } | { kind: 'subdomain' }

const CHOICES: Record<AddressAction, { label: string; description: string }> = {
  'add-website': {
    label: 'A new website',
    description: 'Create a website for this address. You upload its files next.',
  },
  'add-to-website': {
    label: 'One of your websites',
    description: 'Show a website you already have here too. It keeps its other addresses.',
  },
  'add-redirect': {
    label: 'A redirect',
    description: 'Send visitors to another address.',
  },
}

const ORDER: AddressAction[] = ['add-website', 'add-to-website', 'add-redirect']

type FormProps = {
  target: UseTarget
  domain: string
  addresses: Address[]
  actions: AddressAction[]
  onChoose: (action: AddressAction, host: string) => void
}

function UseAddressForm({ target, domain, addresses, actions, onChoose }: FormProps) {
  const choices = ORDER.filter((action) => actions.includes(action))
  const [action, setAction] = useState<AddressAction>(choices[0] ?? 'add-website')
  const [label, setLabel] = useState('')
  const [error, setError] = useState<string | null>(null)

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (target.kind === 'existing') {
      onChoose(action, target.host)
      return
    }
    const result = subdomainHost(label, domain)
    if ('error' in result) {
      setError(result.error)
      return
    }
    const existing = addresses.find((address) => address.host === result.host)
    if (existing && existing.use.kind !== 'none') {
      setError(`${result.host} is already in use, see its row in the list.`)
      return
    }
    onChoose(action, result.host)
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      {target.kind === 'subdomain' ? (
        <Field name="subdomain" invalid={error ? true : undefined}>
          <FieldLabel>Subdomain</FieldLabel>
          <InputGroup className="address-input">
            <Input
              value={label}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              placeholder="shop"
              autoFocus
              onValueChange={(value) => {
                setLabel(value)
                setError(null)
              }}
            />
            <span className="address-suffix">.{domain}</span>
          </InputGroup>
          <FieldDescription>Letters, digits and hyphens, and dots for deeper levels like shop.eu.</FieldDescription>
          {error ? <FieldError match>{error}</FieldError> : null}
        </Field>
      ) : null}
      <Field>
        <FieldLabel className={target.kind === 'existing' ? 'visually-hidden' : undefined}>What should visitors see?</FieldLabel>
        <RadioGroup value={action} onValueChange={(value) => value && setAction(value as AddressAction)}>
          {choices.map((choice) => (
            <Field key={choice}>
              <Radio value={choice}>
                <RadioIndicator />
              </Radio>
              <div className="choice-text">
                <FieldLabel>{CHOICES[choice].label}</FieldLabel>
                <FieldDescription>{CHOICES[choice].description}</FieldDescription>
              </div>
            </Field>
          ))}
        </RadioGroup>
      </Field>
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid">
          Continue
        </Button>
      </div>
    </form>
  )
}

type Props = Omit<FormProps, 'target'> & {
  target: UseTarget | null
  onOpenChange: (open: boolean) => void
}

export function UseAddressDialog({ target, onOpenChange, ...rest }: Props) {
  // Keeps the last target while the dialog closes, so its text doesn't jump.
  const [shown, setShown] = useState(target)
  if (target && target !== shown) setShown(target)
  const title = shown?.kind === 'existing' ? `Use ${shown.host}` : 'Add subdomain'
  const description =
    shown?.kind === 'existing'
      ? 'Choose what visitors see at this address.'
      : `A new address under ${rest.domain}. Choose what visitors see there.`
  return (
    <FormDialog open={target !== null} onOpenChange={onOpenChange} title={title} description={description}>
      {shown ? <UseAddressForm target={shown} {...rest} /> : null}
    </FormDialog>
  )
}
