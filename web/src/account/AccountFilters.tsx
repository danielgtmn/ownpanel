import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from '@ui-sdk/react/collapsible'
import { Field, FieldLabel } from '@ui-sdk/react/field'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { toast } from '@ui-sdk/react/toast'
import { useState, type FormEvent } from 'react'
import { formatDateTime } from '../shared/format/index.ts'
import { useResource } from '../shared/hooks/index.ts'
import {
  formatDay,
  forwardingBody,
  forwardingDraft,
  forwardingError,
  ForwardingFields,
  hasErrors,
  outOfOfficeBody,
  outOfOfficeDraft,
  outOfOfficeErrors,
  OutOfOfficeFields,
  outOfOfficeWhen,
  setByText,
  type ForwardingDraft,
  type OutOfOfficeDraft,
  type OutOfOfficeErrors,
} from '../shared/mailrules/index.ts'
import { ErrorState, LoadingState, Section } from '../shared/ui/index.ts'
import {
  AccountError,
  accountErrorMessage,
  isSessionEnded,
  loadFilters,
  setFilterRules,
  setForwarding,
  setOutOfOffice,
  type FilterRule,
  type Filters,
  type MailboxAccess,
} from './api.ts'
import { FilterRuleDialog } from './FilterRuleDialog.tsx'
import {
  describeRule,
  keyRules,
  moveRule,
  putRule,
  removeRule,
  RULES_MAX,
  ruleTitle,
  rulesBody,
  sameRules,
  type KeyedRule,
} from './filters.ts'

function FormError({ message }: { message?: string | null }) {
  return message ? (
    <p className="form-error" role="alert">
      {message}
    </p>
  ) : null
}

// Resolves with the server's answer, or null when the failure was already shown; throws what the form shows.
// "Your" for the person's own mailbox, else the address of the mailbox they manage ("team@example.com's").
function whose(data: Filters, own: 'Your' | 'your'): string {
  return data.mailbox ? `${data.mailbox}'s` : own
}

export type Save = (run: () => Promise<Filters>) => Promise<Filters | null>

// ---- out-of-office ----

export function OutOfOfficeSection({ data, locked, save }: { data: Filters; locked: boolean; save: Save }) {
  const saved = data.rules.out_of_office
  const [draft, setDraft] = useState<OutOfOfficeDraft>(() => outOfOfficeDraft(saved))
  const [errors, setErrors] = useState<OutOfOfficeErrors>({})
  const [form, setForm] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const setBy = setByText(saved.set_by, saved.set_at, formatDateTime)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = outOfOfficeErrors(draft)
    if (hasErrors(found)) {
      setErrors(found)
      return
    }
    setBusy(true)
    setForm(null)
    let next: Filters | null = null
    try {
      next = await save(() => setOutOfOffice(outOfOfficeBody(draft), !data.rules.managed, data.mailbox))
    } catch (failure) {
      setForm(accountErrorMessage(failure))
    }
    setBusy(false)
    if (!next) return
    const now = next.rules.out_of_office
    setDraft(outOfOfficeDraft(now))
    toast.success(
      now.enabled
        ? `${whose(data, 'Your')} out-of-office reply is on. ${outOfOfficeWhen(draft, formatDay)}.`
        : `${whose(data, 'Your')} out-of-office reply is off.`,
    )
  }

  return (
    <Section
      title="Out-of-office reply"
      description="Answers mail automatically while you're away, for example on holiday."
    >
      <form className="form" onSubmit={submit} noValidate>
        {setBy ? (
          <p className="mailrules-set-by">
            {setBy}.{data.mailbox ? null : ' Saving here makes it yours.'}
          </p>
        ) : null}
        <fieldset className="mailrules-fieldset" disabled={locked || busy}>
          <OutOfOfficeFields
            draft={draft}
            errors={errors}
            switchLabel="Reply automatically"
            switchDescription="People who write to you get this reply, each of them once in the period set below."
            onChange={(change) => {
              setDraft((current) => ({ ...current, ...change }))
              setErrors({})
              setForm(null)
            }}
          />
        </fieldset>
        <FormError message={form} />
        <div className="record-actions">
          <Button type="submit" data-variant="solid" disabled={locked || busy}>
            {busy ? 'Saving…' : 'Save reply'}
          </Button>
        </div>
      </form>
    </Section>
  )
}

// ---- forwarding ----

export function ForwardingSection({ data, locked, save }: { data: Filters; locked: boolean; save: Save }) {
  const saved = data.rules.forwarding
  const [draft, setDraft] = useState<ForwardingDraft>(() => forwardingDraft(saved))
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const setBy = setByText(saved.set_by, saved.set_at, formatDateTime)
  const off = !data.may_forward

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const found = forwardingError(draft)
    if (found) {
      setError(found)
      return
    }
    setBusy(true)
    setForm(null)
    let next: Filters | null = null
    try {
      next = await save(() => setForwarding(forwardingBody(draft), !data.rules.managed, data.mailbox))
    } catch (failure) {
      setForm(accountErrorMessage(failure))
    }
    setBusy(false)
    if (!next) return
    const now = next.rules.forwarding
    setDraft(forwardingDraft(now))
    const mail = `${whose(data, 'Your')} mail`
    toast.success(now.enabled ? `${mail} is now forwarded to ${now.to}.` : `${mail} is no longer forwarded.`)
  }

  return (
    <Section title="Forwarding" description="Sends all your new mail on to another address.">
      <form className="form" onSubmit={submit} noValidate>
        {off ? (
          <p className="notice account-notice-first">
            Forwarding is turned off for this mailbox by the people who manage it.
          </p>
        ) : null}
        {setBy ? (
          <p className="mailrules-set-by">
            {setBy}.{data.mailbox ? null : ' Saving here makes it yours.'}
          </p>
        ) : null}
        <fieldset className="mailrules-fieldset" disabled={locked || off || busy}>
          <ForwardingFields
            draft={draft}
            error={error}
            switchLabel="Forward my mail"
            onChange={(change) => {
              setDraft((current) => ({ ...current, ...change }))
              setError(null)
              setForm(null)
            }}
          />
        </fieldset>
        <FormError message={form} />
        <div className="record-actions">
          <Button type="submit" data-variant="solid" disabled={locked || off || busy}>
            {busy ? 'Saving…' : 'Save forwarding'}
          </Button>
        </div>
      </form>
    </Section>
  )
}

// ---- filters ----

export function FiltersSection({ data, locked, save }: { data: Filters; locked: boolean; save: Save }) {
  const savedRules = data.rules.filters ?? []
  const [list, setList] = useState<KeyedRule[]>(() => keyRules(savedRules))
  const [base, setBase] = useState<FilterRule[]>(savedRules)
  const [editing, setEditing] = useState<{ key: string | null; rule: FilterRule | null } | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [form, setForm] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const folders = data.folders ?? []
  const changed = !sameRules(rulesBody(list), base)

  function open(entry: KeyedRule | null) {
    setEditing(entry ? { key: entry.key, rule: entry.rule } : { key: null, rule: null })
    setDialogOpen(true)
  }

  function update(next: KeyedRule[]) {
    setList(next)
    setForm(null)
  }

  async function remove(entry: KeyedRule, index: number) {
    const yes = await confirm({
      title: `Remove “${ruleTitle(entry.rule, index)}”?`,
      description: 'Mail is no longer sorted by this rule once you save your filters.',
      confirmLabel: 'Remove',
      cancelLabel: 'Keep it',
    })
    if (yes) update(removeRule(list, entry.key))
  }

  async function submit() {
    setBusy(true)
    setForm(null)
    let next: Filters | null = null
    try {
      next = await save(() => setFilterRules(rulesBody(list), !data.rules.managed, data.mailbox))
    } catch (failure) {
      setForm(accountErrorMessage(failure))
    }
    setBusy(false)
    if (!next) return
    const now = next.rules.filters ?? []
    setList(keyRules(now))
    setBase(now)
    const saved = `${whose(data, 'Your')} filters were saved.`
    toast.success(now.length === 0 ? `${saved} No rules sort ${whose(data, 'your')} mail now.` : saved)
  }

  return (
    <Section
      title="Filters"
      description="Sort new mail as it arrives. Rules are tried from the top; only the first rule that fits is used."
      actions={
        <Button disabled={locked || busy || list.length >= RULES_MAX} onClick={() => open(null)}>
          Add rule
        </Button>
      }
    >
      <div className="account-filters">
        {list.length === 0 ? (
          <p className="muted account-empty">
            No rules yet. Add one to move newsletters into a folder, for example.
          </p>
        ) : (
          <ol aria-label="Your filter rules" className="account-rules">
            {list.map((entry, index) => {
              const title = ruleTitle(entry.rule, index)
              const text = describeRule(entry.rule)
              return (
                <li className="account-rule" key={entry.key}>
                  <div className="account-app-main">
                    <strong>{title}</strong>
                    <span className="muted account-rule-text">{text.when}</span>
                    <span className="account-rule-text">{text.then}</span>
                  </div>
                  <div className="account-rule-actions">
                    <Button
                      disabled={locked || busy || index === 0}
                      onClick={() => update(moveRule(list, index, -1))}
                    >
                      Up<span className="visually-hidden"> {title}</span>
                    </Button>
                    <Button
                      disabled={locked || busy || index === list.length - 1}
                      onClick={() => update(moveRule(list, index, 1))}
                    >
                      Down<span className="visually-hidden"> {title}</span>
                    </Button>
                    <Button disabled={locked || busy} onClick={() => open(entry)}>
                      Change<span className="visually-hidden"> {title}</span>
                    </Button>
                    <Button data-variant="destructive" disabled={locked || busy} onClick={() => remove(entry, index)}>
                      Remove<span className="visually-hidden"> {title}</span>
                    </Button>
                  </div>
                </li>
              )
            })}
          </ol>
        )}
        <FormError message={form} />
        <div className="record-actions account-filters-save">
          <Button data-variant="solid" disabled={locked || busy || (!changed && data.rules.managed)} onClick={submit}>
            {busy ? 'Saving…' : 'Save filters'}
          </Button>
          {changed ? <span className="muted">You have changes that aren't saved yet.</span> : null}
        </div>
        {data.rules.script ? (
          <Collapsible className="account-advanced">
            <CollapsibleTrigger>Advanced</CollapsibleTrigger>
            <CollapsiblePanel>
              <p className="muted account-dialog-note">
                {data.rules.managed
                  ? `What this page sets up, as the mail server runs it. Mail programs that manage filters show it under the name “${data.rules.active_script}”.`
                  : `The filters written in a mail program, saved there as “${data.rules.active_script}”, as the mail server runs them.`}
              </p>
              <pre className="mailrules-script">{data.rules.script}</pre>
            </CollapsiblePanel>
          </Collapsible>
        ) : null}
      </div>
      <FilterRuleDialog
        open={dialogOpen}
        rule={editing?.rule ?? null}
        folders={folders}
        mayForward={data.may_forward}
        onOpenChange={setDialogOpen}
        onSubmit={(rule) => {
          update(putRule(list, editing?.key ?? null, rule))
          setDialogOpen(false)
        }}
      />
    </Section>
  )
}

// ---- the three together ----

// The person's own settings and, with a choice above them, those of the mailboxes they manage (above all
// shared ones, which nobody signs in to). managed comes from managedMailboxes.
export function AccountFilters({ managed = [] }: { managed?: MailboxAccess[] }) {
  const [chosen, setChosen] = useState('')
  // A mailbox that is no longer managed falls back to the person's own.
  const mailbox = managed.some((entry) => entry.address === chosen) ? chosen : ''
  return (
    <>
      {managed.length > 0 ? (
        <Section
          title="Out-of-office and filters of other mailboxes"
          description="You may also change the out-of-office reply, forwarding and filters of these mailboxes."
        >
          <RadioGroup
            aria-label="Whose settings to show"
            value={mailbox}
            onValueChange={(value) => setChosen(typeof value === 'string' ? value : '')}
          >
            <Field>
              <Radio value="">
                <RadioIndicator />
              </Radio>
              <FieldLabel>Your mailbox</FieldLabel>
            </Field>
            {managed.map((entry) => (
              <Field key={entry.address}>
                <Radio value={entry.address}>
                  <RadioIndicator />
                </Radio>
                <FieldLabel>
                  {entry.address}
                  {entry.shared ? <span className="muted"> · Shared mailbox</span> : null}
                </FieldLabel>
              </Field>
            ))}
          </RadioGroup>
          {mailbox ? (
            <p className="notice account-notice">
              The settings below are those of {mailbox}. Changes apply to all mail it receives, and you are shown as
              the person who set the out-of-office reply and forwarding.
            </p>
          ) : null}
        </Section>
      ) : null}
      <MailboxFilters key={mailbox} mailbox={mailbox || undefined} />
    </>
  )
}

function MailboxFilters({ mailbox }: { mailbox?: string }) {
  const resource = useResource(() => loadFilters(mailbox), `filters:${mailbox ?? ''}`)
  const [latest, setLatest] = useState<Filters | null>(null)
  const [replace, setReplace] = useState(false)
  const data = latest ?? (resource.status === 'ready' ? resource.data : null)

  if (!data) {
    if (resource.status === 'error') {
      if (isSessionEnded(resource.error)) return null
      return (
        <Section title="Out-of-office reply, forwarding and filters">
          <ErrorState
            title="Couldn't load your filters"
            message={accountErrorMessage(resource.error)}
            onRetry={resource.retry}
          />
        </Section>
      )
    }
    return (
      <Section title="Out-of-office reply, forwarding and filters">
        <LoadingState label="Loading your filters…" />
      </Section>
    )
  }

  const locked = !data.rules.managed && !replace

  // Saves, keeps the server's answer, and on a conflict shows the current state (filters from a mail program
  // arrived meanwhile). Throws other failures for the form to show.
  const save: Save = async (run) => {
    try {
      const next = await run()
      setLatest(next)
      setReplace(false)
      return next
    } catch (error) {
      if (isSessionEnded(error)) return null
      if (error instanceof AccountError && error.status === 409) {
        toast.error(error.message)
        try {
          setLatest(await loadFilters(mailbox))
        } catch {
          // The message above already says what happened.
        }
        setReplace(false)
        return null
      }
      throw error
    }
  }

  async function allowReplace() {
    const yes = await confirm({
      title: 'Replace the filters from your mail program?',
      description:
        'The filters written in your mail program stop working once you save here, and only what you set on this page is used. Your mail program may still show its old filters.',
      confirmLabel: 'Replace them',
      cancelLabel: 'Cancel',
    })
    if (yes) setReplace(true)
  }

  return (
    <>
      {!data.rules.managed ? (
        <Section
          title="Filters from a mail program"
          description="Your out-of-office reply, forwarding and filters were set up in a mail program, so they can't be changed here."
        >
          {replace ? (
            <p className="notice account-notice-first">
              You can change them below now. The filters from your mail program stop working when you save.
            </p>
          ) : (
            <div className="record-actions">
              <Button onClick={allowReplace}>Replace them</Button>
            </div>
          )}
        </Section>
      ) : null}
      <OutOfOfficeSection data={data} locked={locked} save={save} />
      <ForwardingSection data={data} locked={locked} save={save} />
      <FiltersSection data={data} locked={locked} save={save} />
    </>
  )
}
