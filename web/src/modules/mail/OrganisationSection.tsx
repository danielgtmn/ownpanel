import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { toast } from '@ui-sdk/react/toast'
import { useState, type FormEvent } from 'react'
import { errorMessage, isSavedButNotApplied } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { useSession } from '../../shared/session/index.ts'
import { ErrorState, FormDialog, LoadingState, Section } from '../../shared/ui/index.ts'
import {
  createOrganisation,
  deleteOrganisation,
  listOrganisations,
  renameOrganisation,
  setDomainOrganisation,
} from './api.ts'
import {
  ORGANISATION_NAME_MAX,
  organisationDomainsText,
  organisationSentence,
  validateOrganisationName,
} from './rules.ts'
import type { MailDomain, MailOrganisation } from './types.ts'

const BOUNDARY =
  'Mailboxes can only share mail, calendars and contacts with mailboxes of the same organisation. Group domains that belong to one company.'

// The choice in the dialog: "" stands alone, "new" creates one, anything else is an organisation's id.
const NEW = 'new'

function FormError({ message }: { message?: string }) {
  return message ? (
    <p className="form-error" role="alert">
      {message}
    </p>
  ) : null
}

function organisationHint(organisation: MailOrganisation, domain: string, isAdmin: boolean, me: string): string {
  const others = organisation.domains.filter((name) => name !== domain)
  const parts = [others.length > 0 ? `With ${others.join(', ')}.` : 'No other domains yet.']
  if (isAdmin && organisation.owner && organisation.owner !== me) parts.push(`Belongs to ${organisation.owner}.`)
  return parts.join(' ')
}

// Someone else's organisation: the person can only take the domain out.
function LeaveForm({ mail, onSubmit }: { mail: MailDomain; onSubmit: (organisation: string) => Promise<void> }) {
  const [error, setError] = useState<string | undefined>()
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    try {
      await onSubmit('')
    } catch (failure) {
      setError(errorMessage(failure))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <p className="mail-organisation-note">
        An administrator put {mail.domain} into {mail.organisation?.name}. You can't choose another organisation for it,
        only let it stand alone again. Its mailboxes then can't share with the other domains of {mail.organisation?.name}.
      </p>
      <FormError message={error} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Let this domain stand alone'}
        </Button>
      </div>
    </form>
  )
}

function ChooseForm({ mail, onSubmit }: { mail: MailDomain; onSubmit: (organisation: string) => Promise<void> }) {
  const { isAdmin, user } = useSession()
  const organisations = useResource(listOrganisations, 'mail-organisations')
  const [choice, setChoice] = useState(mail.organisation?.id ?? '')
  const [name, setName] = useState('')
  // An organisation created by an earlier try whose grouping failed; used again instead of a second one.
  const [created, setCreated] = useState<{ name: string; id: string } | null>(null)
  const [errors, setErrors] = useState<{ name?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)
  const list = organisations.status === 'ready' ? organisations.data : []
  const me = user.name

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (choice === NEW) {
      const found = validateOrganisationName(name)
      if (found) {
        setErrors({ name: found })
        return
      }
    }
    setErrors({})
    setBusy(true)
    try {
      let id = choice
      if (choice === NEW) {
        const wanted = name.trim()
        if (created && created.name === wanted) {
          id = created.id
        } else {
          const organisation = await createOrganisation(wanted)
          setCreated({ name: wanted, id: organisation.id })
          id = organisation.id
        }
      }
      await onSubmit(id)
    } catch (failure) {
      setErrors({ form: errorMessage(failure) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      {organisations.status === 'loading' ? <LoadingState label="Loading organisations…" /> : null}
      {organisations.status === 'error' ? (
        <ErrorState
          title="Couldn't load the organisations"
          message={errorMessage(organisations.error)}
          onRetry={organisations.retry}
        />
      ) : null}
      {organisations.status === 'ready' ? (
        <Field>
          <FieldLabel>{mail.domain} belongs to</FieldLabel>
          <RadioGroup value={choice} onValueChange={(value) => typeof value === 'string' && setChoice(value)}>
            <Field>
              <Radio value="">
                <RadioIndicator />
              </Radio>
              <div className="choice-text">
                <FieldLabel>Stands alone</FieldLabel>
                <FieldDescription>Its mailboxes share only with each other.</FieldDescription>
              </div>
            </Field>
            {list.map((organisation) => (
              <Field key={organisation.id}>
                <Radio value={organisation.id}>
                  <RadioIndicator />
                </Radio>
                <div className="choice-text">
                  <FieldLabel>{organisation.name}</FieldLabel>
                  <FieldDescription>{organisationHint(organisation, mail.domain, isAdmin, me)}</FieldDescription>
                </div>
              </Field>
            ))}
            <Field>
              <Radio value={NEW}>
                <RadioIndicator />
              </Radio>
              <div className="choice-text">
                <FieldLabel>A new organisation</FieldLabel>
                <FieldDescription>Other domains can join it later.</FieldDescription>
              </div>
            </Field>
          </RadioGroup>
        </Field>
      ) : null}
      {choice === NEW ? (
        <Field name="organisation_name" invalid={errors.name ? true : undefined}>
          <FieldLabel>Name of the organisation</FieldLabel>
          <Input
            value={name}
            placeholder="Example Ltd"
            maxLength={ORGANISATION_NAME_MAX}
            autoComplete="off"
            onValueChange={(value) => {
              setName(value)
              setErrors({})
            }}
          />
          <FieldDescription>Usually the name of the company.</FieldDescription>
          {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
        </Field>
      ) : null}
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy || organisations.status !== 'ready'}>
          {busy ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

function RenameForm({ current, onSubmit }: { current: string; onSubmit: (name: string) => Promise<void> }) {
  const [name, setName] = useState(current)
  const [errors, setErrors] = useState<{ name?: string; form?: string }>({})
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = validateOrganisationName(name)
    if (found) {
      setErrors({ name: found })
      return
    }
    setErrors({})
    setBusy(true)
    try {
      await onSubmit(name.trim())
    } catch (failure) {
      setErrors({ form: errorMessage(failure) })
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field name="organisation_name" invalid={errors.name ? true : undefined}>
        <FieldLabel>Name of the organisation</FieldLabel>
        <Input
          value={name}
          maxLength={ORGANISATION_NAME_MAX}
          autoComplete="off"
          autoFocus
          onValueChange={(value) => {
            setName(value)
            setErrors({})
          }}
        />
        {errors.name ? <FieldError match>{errors.name}</FieldError> : null}
      </Field>
      <FormError message={errors.form} />
      <div className="form-actions">
        <DialogClose disabled={busy}>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" disabled={busy}>
          {busy ? 'Saving…' : 'Rename'}
        </Button>
      </div>
    </form>
  )
}

export function OrganisationSection({ mail, onChange }: { mail: MailDomain; onChange: () => void }) {
  const [open, setOpen] = useState(false)
  const [renaming, setRenaming] = useState(false)
  const organisation = mail.organisation
  const foreign = organisation !== null && !organisation.own
  const manages = organisation !== null && organisation.own

  async function rename(name: string) {
    if (!organisation) return
    if (name !== organisation.name) {
      await renameOrganisation(organisation.id, name)
      toast.success(`The organisation is now called ${name}.`)
    }
    setRenaming(false)
    onChange()
  }

  async function remove() {
    if (!organisation) return
    const yes = await confirm({
      title: `Delete the organisation ${organisation.name}?`,
      description: 'Its domains then stand alone; their mailboxes can no longer share with each other.',
      confirmLabel: 'Delete organisation',
      cancelLabel: 'Cancel',
    })
    if (!yes) return
    try {
      await deleteOrganisation(organisation.id)
      toast.success(`The organisation ${organisation.name} was deleted.`)
    } catch (error) {
      if (isSavedButNotApplied(error)) toast.warning(errorMessage(error))
      else toast.error(errorMessage(error))
    }
    onChange()
  }

  async function save(id: string) {
    if (id === (organisation?.id ?? '')) {
      setOpen(false)
      return
    }
    try {
      const next = await setDomainOrganisation(mail.domain, id)
      toast.success(
        next.organisation ? `${mail.domain} is now part of ${next.organisation.name}.` : `${mail.domain} now stands alone.`,
      )
    } catch (error) {
      if (!isSavedButNotApplied(error)) throw error
      toast.warning(errorMessage(error))
    }
    setOpen(false)
    onChange()
  }

  return (
    <Section
      title="Organisation"
      description={BOUNDARY}
      actions={
        <>
          <Button onClick={() => setOpen(true)}>
            {foreign ? 'Let it stand alone' : 'Change'}
            <span className="visually-hidden"> the organisation of {mail.domain}</span>
          </Button>
          {manages ? (
            <>
              <Button onClick={() => setRenaming(true)}>
                Rename<span className="visually-hidden"> the organisation {organisation.name}</span>
              </Button>
              <Button data-variant="destructive" pendingAnnouncement="Deleting" onClick={remove}>
                Delete organisation
              </Button>
            </>
          ) : null}
        </>
      }
    >
      <div className="mail-organisation">
        <p>{organisationSentence(organisation)}</p>
        {organisation ? <p className="muted">{organisationDomainsText(organisation)}</p> : null}
        {foreign ? <p className="muted">An administrator set this.</p> : null}
      </div>
      <FormDialog
        open={open}
        onOpenChange={setOpen}
        title={foreign ? 'Let this domain stand alone?' : `Organisation of ${mail.domain}`}
        description={BOUNDARY}
      >
        {foreign ? <LeaveForm mail={mail} onSubmit={save} /> : <ChooseForm mail={mail} onSubmit={save} />}
      </FormDialog>
      <FormDialog
        open={renaming}
        onOpenChange={setRenaming}
        title="Rename the organisation"
        description="The new name is shown for every domain of the organisation."
      >
        <RenameForm current={organisation?.name ?? ''} onSubmit={rename} />
      </FormDialog>
    </Section>
  )
}
