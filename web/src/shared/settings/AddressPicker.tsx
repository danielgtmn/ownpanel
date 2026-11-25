import { Button } from '@ui-sdk/react/button'
import { Checkbox, CheckboxGroup, CheckboxIndicator } from '@ui-sdk/react/checkbox'
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from '@ui-sdk/react/collapsible'
import { Field, FieldDescription, FieldError, FieldLabel, FieldLegend, FieldSet } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useState, type KeyboardEvent } from 'react'
import type { PanelSettings } from './api.ts'
import { LockedField } from './LockedField.tsx'
import { addressOptions, normalizeAddress, validateAddress, type AddressOption } from './rules.ts'

export const ADDRESSES_PURPOSE =
  'The domains of your sites and of the panel must point to these addresses, so visitors reach this server.'

type Props = {
  settings: PanelSettings
  selected: string[]
  onSelectedChange: (selected: string[]) => void
  extra: string[]
  onExtraChange: (extra: string[]) => void
  error?: string
}

function AddressOptionRow({ option, disabled }: { option: AddressOption; disabled: boolean }) {
  return (
    <label data-slot="label" data-private={option.public ? undefined : ''}>
      <Checkbox value={option.ip} disabled={disabled}>
        <CheckboxIndicator />
      </Checkbox>
      <code className="address-option-text">{option.ip}</code>
    </label>
  )
}

export function AddressPicker({ settings, selected, onSelectedChange, extra, onExtraChange, error }: Props) {
  const [draft, setDraft] = useState('')
  const [draftError, setDraftError] = useState<string | null>(null)

  if (settings.server_ips_locked) {
    return <LockedField label="Server addresses" value={settings.server_ips.join(', ')} />
  }

  const options = addressOptions(settings, extra)
  const privateBlocked = settings.tls_mode === 'acme'
  const publicOptions = options.filter((option) => option.public)
  const privateOptions = options.filter((option) => !option.public)

  function add() {
    const problem = validateAddress(draft, options.map((option) => option.ip))
    if (problem) {
      setDraftError(problem)
      return
    }
    const ip = normalizeAddress(draft)
    onExtraChange([...extra, ip])
    onSelectedChange([...selected, ip])
    setDraft('')
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key !== 'Enter') return
    event.preventDefault()
    add()
  }

  return (
    <div className="address-picker">
      <FieldSet>
        <FieldLegend className="visually-hidden">Server addresses</FieldLegend>
        {options.length === 0 ? (
          <p className="notice">No addresses were found on this server. Add its public IP address below.</p>
        ) : (
          <CheckboxGroup className="address-options" value={selected} onValueChange={onSelectedChange}>
            {publicOptions.map((option) => (
              <AddressOptionRow key={option.ip} option={option} disabled={false} />
            ))}
            {privateOptions.length > 0 ? (
              <Collapsible className="address-private" defaultOpen={publicOptions.length === 0 || privateOptions.some((option) => selected.includes(option.ip))}>
                <CollapsibleTrigger>Private addresses ({privateOptions.length})</CollapsibleTrigger>
                <CollapsiblePanel>
                  <p className="muted">
                    Not reachable from the internet, only from a local network.
                    {privateBlocked ? ' They can’t be used while certificates are requested automatically.' : ''}
                  </p>
                  {privateOptions.map((option) => (
                    <AddressOptionRow key={option.ip} option={option} disabled={privateBlocked} />
                  ))}
                </CollapsiblePanel>
              </Collapsible>
            ) : null}
          </CheckboxGroup>
        )}
        {error ? (
          <p className="form-error" role="alert">
            {error}
          </p>
        ) : null}
      </FieldSet>
      <Field className="address-add" invalid={draftError ? true : undefined}>
        <FieldLabel>Add an address</FieldLabel>
        <div className="inline-control">
          <Input
            value={draft}
            placeholder="203.0.113.5"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            onKeyDown={onKeyDown}
            onValueChange={(value) => {
              setDraft(value)
              setDraftError(null)
            }}
          />
          <Button onClick={add}>Add</Button>
        </div>
        <FieldDescription>For example when this server sits behind a router that forwards to it.</FieldDescription>
        {draftError ? <FieldError match>{draftError}</FieldError> : null}
      </Field>
    </div>
  )
}
