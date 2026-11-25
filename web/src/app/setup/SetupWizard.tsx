import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { Field, FieldDescription, FieldError, FieldLabel } from '@ui-sdk/react/field'
import { Input } from '@ui-sdk/react/input'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { markSetupComplete } from '../../shared/session/index.ts'
import {
  AddressPicker,
  ADDRESSES_PURPOSE,
  CERTIFICATE_EMAIL_LABEL,
  finishWizard,
  getPanelChecks,
  getSettings,
  initialAddressSelection,
  LockedField,
  PanelAddressDetails,
  panelUrl,
  sameAddresses,
  saveSettings,
  type PanelSettings,
  warnIfNotApplied,
} from '../../shared/settings/index.ts'
import { ErrorState, LoadingState } from '../../shared/ui/index.ts'
import { StepIndicator } from './StepIndicator.tsx'
import {
  canContinue,
  canSkip,
  checkPanelStep,
  continueHint,
  initialStep,
  nextStep,
  previousStep,
  REDIRECT_DELAY,
  runFinish,
  SKIP_NOTE,
  type PanelDraft,
  type StepId,
} from './wizard.ts'
import './setup.css'

type Finished = { outcome: 'open' | 'wait'; domain: string; url: string }

function FinishedView({ finished }: { finished: Finished }) {
  const headingRef = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    headingRef.current?.focus()
    if (finished.outcome !== 'open') return
    const timer = setTimeout(() => window.location.assign(finished.url), REDIRECT_DELAY)
    return () => clearTimeout(timer)
  }, [finished])

  return (
    <div className="wizard-step">
      <h2 ref={headingRef} tabIndex={-1}>
        {finished.outcome === 'open' ? 'Your panel is ready' : 'Setup finished'}
      </h2>
      {finished.outcome === 'open' ? (
        <p>From now on, this IP address sends you to {finished.domain}. Taking you there in a few seconds.</p>
      ) : (
        <p>
          From now on, this IP address sends you to {finished.domain}. That works once the domain points to this server
          and its certificate is ready, usually a few minutes after the DNS records are in place. Until then the link below
          won't open yet; try again a little later.
        </p>
      )}
      <Button data-variant="solid" className="wizard-open" render={<a href={finished.url} />}>
        Open your panel at {finished.url}
      </Button>
    </div>
  )
}

function Summary({ settings }: { settings: PanelSettings }) {
  return (
    <dl className="wizard-summary">
      <div>
        <dt>Server addresses</dt>
        <dd>{settings.server_ips.length > 0 ? settings.server_ips.join(', ') : 'None chosen'}</dd>
      </div>
      <div>
        <dt>Panel address</dt>
        <dd>
          {settings.panel_domain
            ? `https://${settings.panel_domain}`
            : 'No domain. The panel stays reachable by IP address.'}
        </dd>
      </div>
      <div>
        <dt>E-mail for certificate notices</dt>
        <dd>{settings.acme_email || 'Not set'}</dd>
      </div>
    </dl>
  )
}

type PanelErrors = { domain?: string; email?: string; form?: string }

function PanelStepFields({
  settings,
  draft,
  onDraftChange,
  errors,
}: {
  settings: PanelSettings
  draft: PanelDraft
  onDraftChange: (draft: PanelDraft, field: keyof PanelDraft) => void
  errors: PanelErrors
}) {
  return (
    <div className="wizard-fields">
      {settings.panel_domain_locked ? (
        <LockedField label="Panel domain" value={settings.panel_domain} />
      ) : (
        <Field name="panel-domain" invalid={errors.domain ? true : undefined}>
          <FieldLabel>Panel domain</FieldLabel>
          <Input
            value={draft.domain}
            placeholder="panel.example.com"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            onValueChange={(domain) => onDraftChange({ ...draft, domain }, 'domain')}
          />
          <FieldDescription>Use a subdomain of a domain you own, for example panel.example.com.</FieldDescription>
          {errors.domain ? <FieldError match>{errors.domain}</FieldError> : null}
        </Field>
      )}
      {settings.acme_email_locked ? (
        <LockedField label={CERTIFICATE_EMAIL_LABEL} value={settings.acme_email} />
      ) : (
        <Field name="certificate-email" invalid={errors.email ? true : undefined}>
          <FieldLabel>{CERTIFICATE_EMAIL_LABEL}</FieldLabel>
          <Input
            type="email"
            value={draft.email}
            placeholder="you@example.com"
            autoComplete="email"
            onValueChange={(email) => onDraftChange({ ...draft, email }, 'email')}
          />
          <FieldDescription>
            Optional. The service that issues the certificates may write to this address about them, for example before
            one expires.
          </FieldDescription>
          {errors.email ? <FieldError match>{errors.email}</FieldError> : null}
        </Field>
      )}
      {errors.form ? (
        <p className="form-error" role="alert">
          {errors.form}
        </p>
      ) : null}
    </div>
  )
}

function Wizard({ initial }: { initial: PanelSettings }) {
  const [settings, setSettings] = useState(initial)
  const [step, setStep] = useState<StepId>(() => initialStep(initial))
  const [selected, setSelected] = useState(() => initialAddressSelection(initial))
  const [extra, setExtra] = useState<string[]>([])
  const [addressError, setAddressError] = useState<string | undefined>()
  const [panelDraft, setPanelDraft] = useState<PanelDraft>({ domain: initial.panel_domain, email: initial.acme_email })
  const [panelErrors, setPanelErrors] = useState<PanelErrors>({})
  const [finished, setFinished] = useState<Finished | null>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const firstRender = useRef(true)

  useEffect(() => {
    if (firstRender.current) {
      firstRender.current = false
      return
    }
    headingRef.current?.focus()
  }, [step])

  const context = { settings, selectedAddresses: selected }
  const allowed = canContinue(step, context)
  const skippable = canSkip(step, settings)
  const hint = skippable ? SKIP_NOTE : continueHint(step, context)

  async function continueFromAddresses() {
    if (!settings.server_ips_locked && !sameAddresses(selected, settings.server_ips)) {
      try {
        const result = await saveSettings({ server_ips: selected })
        setSettings(result.settings)
        warnIfNotApplied(result)
      } catch (error) {
        setAddressError(errorMessage(error))
        return
      }
    }
    setAddressError(undefined)
    setStep(nextStep('addresses'))
  }

  async function continueFromPanel() {
    const check = checkPanelStep(settings, panelDraft)
    if (check.domainError || check.emailError) {
      setPanelErrors({ domain: check.domainError ?? undefined, email: check.emailError ?? undefined })
      return
    }
    if (Object.keys(check.change).length > 0) {
      try {
        const result = await saveSettings(check.change)
        setSettings(result.settings)
        setPanelDraft({ domain: result.settings.panel_domain, email: result.settings.acme_email })
        warnIfNotApplied(result)
      } catch (error) {
        setPanelErrors({ form: errorMessage(error) })
        return
      }
    }
    setPanelErrors({})
    setStep(nextStep('panel'))
  }

  async function goOn() {
    if (step === 'addresses') return continueFromAddresses()
    if (step === 'panel') return continueFromPanel()
    setStep(nextStep(step))
  }

  function skip() {
    setPanelDraft({ domain: settings.panel_domain, email: settings.acme_email })
    setPanelErrors({})
    setStep('done')
  }

  async function finish() {
    try {
      const done = await runFinish(settings, { loadChecks: getPanelChecks, ask: confirm, complete: finishWizard })
      if (done.status === 'cancelled') return
      warnIfNotApplied(done.result)
      if (done.outcome === 'app') {
        toast.success('Setup finished.')
        window.location.hash = ''
        markSetupComplete()
        return
      }
      const domain = done.result.settings.panel_domain
      setFinished({ outcome: done.outcome, domain, url: panelUrl(domain, window.location.port) })
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }

  // The panel step is one form, so Enter in a field continues like the Continue button.
  const asForm = !finished && step === 'panel'
  const Card = asForm ? 'form' : 'div'

  return (
    <div className="gate wizard">
      <Card
        className="gate-card wizard-card"
        noValidate={asForm ? true : undefined}
        onSubmit={asForm ? (event: FormEvent) => event.preventDefault() : undefined}
      >
        <div className="gate-head">
          <span className="brand">ownpanel</span>
          <h1>Set up your panel</h1>
        </div>
        <StepIndicator current={finished ? 'done' : step} />

        {finished ? (
          <FinishedView finished={finished} />
        ) : step === 'addresses' ? (
          <section className="wizard-step" aria-labelledby="wizard-heading">
            <div className="wizard-intro">
              <h2 id="wizard-heading" ref={headingRef} tabIndex={-1}>
                Where can this server be reached?
              </h2>
              <p className="muted">
                {ADDRESSES_PURPOSE} Choose the IP addresses of this server that are reachable from the internet.
              </p>
            </div>
            <AddressPicker
              settings={settings}
              selected={selected}
              onSelectedChange={(next) => {
                setSelected(next)
                setAddressError(undefined)
              }}
              extra={extra}
              onExtraChange={setExtra}
              error={addressError}
            />
          </section>
        ) : step === 'panel' ? (
          <section className="wizard-step" aria-labelledby="wizard-heading">
            <div className="wizard-intro">
              <h2 id="wizard-heading" ref={headingRef} tabIndex={-1}>
                Where will you open the panel?
              </h2>
              <p className="muted">
                With a domain, the panel gets a trusted certificate and browsers stop warning.
              </p>
            </div>
            <PanelStepFields
              settings={settings}
              draft={panelDraft}
              onDraftChange={(next, field) => {
                setPanelDraft(next)
                setPanelErrors((current) => ({ ...current, [field]: undefined, form: undefined }))
              }}
              errors={panelErrors}
            />
          </section>
        ) : (
          <section className="wizard-step" aria-labelledby="wizard-heading">
            <div className="wizard-intro">
              <h2 id="wizard-heading" ref={headingRef} tabIndex={-1}>
                Check your choices
              </h2>
              <p className="muted">You can change them later under Settings.</p>
            </div>
            <Summary settings={settings} />
            <PanelAddressDetails settings={settings} />
            {settings.panel_domain ? (
              <p>After you finish, this IP address sends you to {settings.panel_domain}.</p>
            ) : null}
          </section>
        )}

        {finished ? null : (
          <div className="wizard-actions">
            {hint ? <p className="muted wizard-hint">{hint}</p> : null}
            {step !== 'addresses' ? <Button onClick={() => setStep(previousStep(step))}>Back</Button> : null}
            {skippable ? <Button onClick={skip}>Skip for now</Button> : null}
            {step === 'done' ? (
              <Button data-variant="solid" pendingAnnouncement="Finishing" onClick={finish}>
                Finish setup
              </Button>
            ) : (
              <Button
                type={asForm ? 'submit' : 'button'}
                data-variant="solid"
                pendingAnnouncement="Saving"
                disabled={!allowed}
                onClick={goOn}
              >
                Continue
              </Button>
            )}
          </div>
        )}
      </Card>
    </div>
  )
}

export function SetupWizard() {
  const settings = useResource(getSettings, 'settings')
  usePageTitle('Set up')

  if (settings.status === 'loading') {
    return (
      <div className="gate">
        <LoadingState label="Loading…" />
      </div>
    )
  }
  if (settings.status === 'error') {
    return (
      <div className="gate">
        <ErrorState title="Couldn't load the setup" message={errorMessage(settings.error)} onRetry={settings.retry} />
      </div>
    )
  }
  return <Wizard initial={settings.data} />
}
