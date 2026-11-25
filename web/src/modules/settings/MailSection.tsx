import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import {
  NumberInput,
  NumberInputControl,
  NumberInputDecrement,
  NumberInputGroup,
  NumberInputIncrement,
} from '@ui-sdk/react/number-input'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { Switch, SwitchThumb } from '@ui-sdk/react/switch'
import { Textarea } from '@ui-sdk/react/textarea'
import { useState, type FormEvent, type ReactNode } from 'react'
import { errorMessage, isNotFound } from '../../shared/api/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import {
  announceSaved,
  LOCKED_NOTE,
  normalizeDomain,
  saveSettings,
  validateEmail,
  type PanelSettings,
} from '../../shared/settings/index.ts'
import { FormDialog, Section, StatusBadge } from '../../shared/ui/index.ts'
import { getMailHealth, healthLine, validateMailHost } from './mailHealth.ts'
import {
  JUNK_MAX,
  JUNK_MIN,
  MAX_LISTS,
  spamChange,
  spamDraft,
  spamSummary,
  validateSpamDraft,
  type ListMode,
  type SpamDraft,
  type SpamErrors,
} from './spamChecks.ts'

type FormProps = { settings: PanelSettings; onSaved: (settings: PanelSettings) => void }
type DialogFormProps = FormProps & { onDone: () => void }

const HOST_LABEL = 'Name of the mail server'

function MailHostForm({ settings, onSaved, onDone }: DialogFormProps) {
  const [draft, setDraft] = useState(settings.mail_host)
  const [error, setError] = useState<string | null>(null)

  async function save() {
    const host = normalizeDomain(draft)
    const problem = validateMailHost(host)
    if (problem) {
      setError(problem)
      return
    }
    if (host === settings.mail_host) {
      onDone()
      return
    }
    if (settings.mail_host) {
      const yes = await confirm({
        title: host ? `Rename the mail server to ${host}?` : 'Remove the name of the mail server?',
        description: host
          ? 'Mail programs and the mail records of your domains have to use the new name. Mail for domains whose records still name the old one may not arrive.'
          : 'Without a name, no domain starts receiving mail here.',
        confirmLabel: host ? 'Rename' : 'Remove name',
        cancelLabel: 'Cancel',
      })
      if (!yes) return
    }
    try {
      const result = await saveSettings({ mail_host: host })
      setDraft(result.settings.mail_host)
      onSaved(result.settings)
      announceSaved(result, host ? `The mail server is now called ${host}.` : 'Removed the name of the mail server.')
      onDone()
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <form className="form" onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field invalid={error ? true : undefined}>
        <FieldLabel>{HOST_LABEL}</FieldLabel>
        <Input
          value={draft}
          placeholder="mail.example.com"
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          autoFocus
          onValueChange={(value) => {
            setDraft(value)
            setError(null)
          }}
        />
        <FieldDescription>Use a name of its own, like mail.example.com, that points to this server.</FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" pendingAnnouncement="Saving" onClick={save}>
          Save name
        </Button>
      </div>
    </form>
  )
}

function PostmasterForm({ settings, onSaved, onDone }: DialogFormProps) {
  const [draft, setDraft] = useState(settings.postmaster_address)
  const [error, setError] = useState<string | null>(null)

  async function save() {
    const address = draft.trim().toLowerCase()
    const problem = validateEmail(address)
    if (problem) {
      setError(problem)
      return
    }
    if (address === settings.postmaster_address) {
      onDone()
      return
    }
    try {
      const result = await saveSettings({ postmaster_address: address })
      setDraft(result.settings.postmaster_address)
      onSaved(result.settings)
      announceSaved(result, address ? `Reports about mail problems go to ${address}.` : 'Removed the postmaster address.')
      onDone()
    } catch (caught) {
      setError(errorMessage(caught))
    }
  }

  return (
    <form className="form" onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field invalid={error ? true : undefined}>
        <FieldLabel>Postmaster address</FieldLabel>
        <Input
          type="email"
          value={draft}
          placeholder="you@example.com"
          autoComplete="email"
          autoFocus
          onValueChange={(value) => {
            setDraft(value)
            setError(null)
          }}
        />
        <FieldDescription>Leave it empty to stop forwarding these reports.</FieldDescription>
        {error ? <FieldError match>{error}</FieldError> : null}
      </Field>
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" pendingAnnouncement="Saving" onClick={save}>
          Save address
        </Button>
      </div>
    </form>
  )
}

function SpamChecksForm({ settings, onSaved, onDone }: DialogFormProps) {
  const [draft, setDraft] = useState<SpamDraft>(() => spamDraft(settings))
  const [errors, setErrors] = useState<SpamErrors>({})
  const [formError, setFormError] = useState<string | null>(null)
  const change = spamChange(draft, settings)
  const unchanged = Object.keys(change).length === 0

  function update(next: Partial<SpamDraft>) {
    setDraft((current) => ({ ...current, ...next }))
    setErrors({})
    setFormError(null)
  }

  async function save() {
    const problems = validateSpamDraft(draft)
    if (problems.lists || problems.junkScore) {
      setErrors(problems)
      return
    }
    if (unchanged) {
      onDone()
      return
    }
    try {
      const result = await saveSettings(change)
      onSaved(result.settings)
      announceSaved(result, 'Saved the spam checks.')
      onDone()
    } catch (caught) {
      setFormError(errorMessage(caught))
    }
  }

  return (
    <form className="form spam-checks" onSubmit={(event: FormEvent) => event.preventDefault()} noValidate>
      <Field>
        <FieldLabel>Lists of known spam senders</FieldLabel>
        <FieldDescription>Mail from servers on these lists is turned away or counts towards Junk.</FieldDescription>
        <RadioGroup value={draft.mode} onValueChange={(value) => value && update({ mode: value as ListMode })}>
          <Field>
            <Radio value="default">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Use the recommended list</FieldLabel>
              <FieldDescription>The list most mail servers use, zen.spamhaus.org.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="own">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Use my own lists</FieldLabel>
              <FieldDescription>Up to {MAX_LISTS} lists, one name per line.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="off">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Don't check</FieldLabel>
              <FieldDescription>Mail isn't compared against any list.</FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>
      {draft.mode === 'own' ? (
        <Field invalid={errors.lists ? true : undefined}>
          <FieldLabel>Lists</FieldLabel>
          <Textarea
            value={draft.lists}
            rows={3}
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="zen.spamhaus.org"
            onValueChange={(value) => update({ lists: value })}
          />
          <FieldDescription>
            If you have a free Spamhaus key, enter your key followed by .zen.dq.spamhaus.net. The key is kept secret and
            shown as ••••.
          </FieldDescription>
          {errors.lists ? <FieldError match>{errors.lists}</FieldError> : null}
        </Field>
      ) : null}
      <Field invalid={errors.junkScore ? true : undefined}>
        <FieldLabel>Points for Junk</FieldLabel>
        <NumberInput
          value={draft.junkScore}
          min={JUNK_MIN}
          max={JUNK_MAX}
          step={0.5}
          format={{ useGrouping: false, maximumFractionDigits: 1 }}
          onValueChange={(value) => update({ junkScore: value })}
        >
          <NumberInputGroup>
            <NumberInputDecrement aria-label="Fewer points" />
            <NumberInputControl />
            <NumberInputIncrement aria-label="More points" />
          </NumberInputGroup>
        </NumberInput>
        <FieldDescription>
          Every sign of spam adds points, like a sender that can't be checked or a server on a list. Mail with this many
          points goes to the Junk folder instead of the inbox. Fewer points catch more spam, but also more wanted mail.
          Usually 5.
        </FieldDescription>
        {errors.junkScore ? <FieldError match>{errors.junkScore}</FieldError> : null}
      </Field>
      <Field className="update-switch">
        <Switch checked={draft.greylisting} onCheckedChange={(next) => update({ greylisting: next === true })}>
          <SwitchThumb />
        </Switch>
        <div className="choice-text">
          <FieldLabel>Hold back first mail from unknown servers</FieldLabel>
          <FieldDescription>
            The first message from a server that can't show it may send for its domain is accepted only when it tries
            again. Real mail servers do that on their own, so such mail arrives some minutes later; much spam never
            comes back.
          </FieldDescription>
        </div>
      </Field>
      {formError ? (
        <Field invalid>
          <FieldError match>{formError}</FieldError>
        </Field>
      ) : null}
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" pendingAnnouncement="Saving" onClick={save}>
          Save spam checks
        </Button>
      </div>
    </form>
  )
}

function MailHealthList({ version }: { version: string }) {
  const health = useResource(getMailHealth, `mail-health:${version}`)
  if (health.status === 'loading') return <p className="muted">Checking the mail server…</p>
  if (health.status === 'error') {
    if (isNotFound(health.error)) return null
    return <p className="muted">Couldn't check the mail server. {errorMessage(health.error)}</p>
  }
  const host = health.data.mail_host
  return (
    <div className="mail-health">
      <div className="mail-health-head">
        <h3>How the mail server is doing</h3>
        <Button pendingAnnouncement="Checking" onClick={() => health.reload()}>
          Check again
        </Button>
      </div>
      <ul className="mail-health-list" aria-label="How the mail server is doing">
        {health.data.checks.map((check) => {
          const line = healthLine(check, host)
          return (
            <li key={check.id} className="mail-health-item">
              <div className="mail-health-row">
                <span className="mail-health-title">{line.title}</span>
                <StatusBadge tone={line.tone}>{line.badge}</StatusBadge>
              </div>
              {line.hint ? <p className="muted">{line.hint}</p> : null}
              {check.detail && check.state !== 'ok' && !line.ownDetail ? <p className="muted">{check.detail}</p> : null}
            </li>
          )
        })}
      </ul>
    </div>
  )
}

type RowProps = {
  label: string
  description: string
  locked?: boolean
  dialog: { title: string; description: string }
  children: ReactNode
  form: (onDone: () => void) => ReactNode
}

/** One setting: its current value on the page, changed in a dialog. */
function SettingRow({ label, description, locked, dialog, children, form }: RowProps) {
  const [open, setOpen] = useState(false)
  return (
    <li className="mail-setting">
      <div className="mail-setting-main">
        <h3>{label}</h3>
        <div className="mail-setting-value">{children}</div>
        <p className="muted">{locked ? `${LOCKED_NOTE}, so it can't be changed here.` : description}</p>
      </div>
      {locked ? null : (
        <Button onClick={() => setOpen(true)}>
          Change<span className="visually-hidden"> {label.toLowerCase()}</span>
        </Button>
      )}
      <FormDialog open={open} onOpenChange={setOpen} title={dialog.title} description={dialog.description}>
        {form(() => setOpen(false))}
      </FormDialog>
    </li>
  )
}

export function MailSection({ settings, onSaved }: FormProps) {
  return (
    <Section
      title="E-mail"
      description="The mail server that receives and sends the mail of the domains with e-mail turned on."
    >
      <div className="settings-stack">
        <ul className="mail-settings" aria-label="Mail server settings">
          <SettingRow
            label={HOST_LABEL}
            description="Mail programs connect to this name, and the mail records of your domains point to it."
            locked={settings.mail_host_locked}
            dialog={{
              title: HOST_LABEL,
              description: 'Mail programs connect to this name, and the mail records of your domains point to it.',
            }}
            form={(onDone) => <MailHostForm settings={settings} onSaved={onSaved} onDone={onDone} />}
          >
            {settings.mail_host ? <code>{settings.mail_host}</code> : <span>Not set yet. No domain receives mail until it is.</span>}
          </SettingRow>
          <SettingRow
            label="Postmaster address"
            description="Other mail servers send reports about problems to postmaster@ and abuse@; they go on to this address."
            dialog={{
              title: 'Postmaster address',
              description: 'Receives mail to postmaster@ and abuse@ of the mail server, where other mail servers report problems.',
            }}
            form={(onDone) => <PostmasterForm settings={settings} onSaved={onSaved} onDone={onDone} />}
          >
            {settings.postmaster_address ? <span>{settings.postmaster_address}</span> : <span>Not set</span>}
          </SettingRow>
          <SettingRow
            label="Spam checks"
            description="How incoming mail is checked before it reaches a mailbox."
            dialog={{ title: 'Spam checks', description: 'How incoming mail is checked before it reaches a mailbox.' }}
            form={(onDone) => <SpamChecksForm settings={settings} onSaved={onSaved} onDone={onDone} />}
          >
            <span>{spamSummary(settings)}</span>
          </SettingRow>
        </ul>
        <MailHealthList
          version={`${settings.mail_host}|${settings.postmaster_address}|${settings.mail_blocklists.join(',')}`}
        />
      </div>
    </Section>
  )
}
