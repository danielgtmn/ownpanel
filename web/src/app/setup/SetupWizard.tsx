import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { toast } from '@ui-sdk/react/toast'
import { useEffect, useRef, useState } from 'react'
import { errorMessage } from '../../shared/api/index.ts'
import { usePageTitle, useResource } from '../../shared/hooks/index.ts'
import { markSetupComplete } from '../../shared/session/index.ts'
import {
  AddressPicker,
  ADDRESSES_PURPOSE,
  CertificateEmailForm,
  finishWizard,
  getPanelChecks,
  getSettings,
  initialAddressSelection,
  PanelAddressDetails,
  PanelDomainForm,
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
  continueHint,
  initialStep,
  nextStep,
  previousStep,
  REDIRECT_DELAY,
  runFinish,
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

function Wizard({ initial }: { initial: PanelSettings }) {
  const [settings, setSettings] = useState(initial)
  const [step, setStep] = useState<StepId>(() => initialStep(initial))
  const [selected, setSelected] = useState(() => initialAddressSelection(initial))
  const [extra, setExtra] = useState<string[]>([])
  const [addressError, setAddressError] = useState<string | undefined>()
  const [domainDirty, setDomainDirty] = useState(false)
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

  const context = { settings, selectedAddresses: selected, domainDirty }
  const allowed = canContinue(step, context)
  const hint = continueHint(step, context)

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

  async function goOn() {
    if (step === 'addresses') return continueFromAddresses()
    setStep(nextStep(step))
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

  return (
    <div className="gate wizard">
      <div className="gate-card wizard-card">
        <div className="gate-head">
          <span className="brand">ownpanel</span>
          <h1>Set up your panel</h1>
        </div>
        <StepIndicator current={finished ? 'done' : step} />

        {finished ? (
          <FinishedView finished={finished} />
        ) : step === 'addresses' ? (
          <section className="wizard-step" aria-labelledby="wizard-heading">
            <h2 id="wizard-heading" ref={headingRef} tabIndex={-1}>
              Server addresses
            </h2>
            <p className="muted">
              These are the IP addresses of this server. {ADDRESSES_PURPOSE} Choose the ones that are reachable from the
              internet.
            </p>
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
            <h2 id="wizard-heading" ref={headingRef} tabIndex={-1}>
              Panel address
            </h2>
            <p className="muted">
              Choose the domain you'll open the panel at. With a domain, the panel gets a trusted certificate and browsers
              stop warning.
            </p>
            <PanelDomainForm settings={settings} onSaved={setSettings} onDirtyChange={setDomainDirty} />
            <PanelAddressDetails settings={settings} />
            <CertificateEmailForm settings={settings} onSaved={setSettings} />
            {canSkip(step, settings) ? (
              <div className="wizard-skip">
                <Button className="link-button" onClick={() => setStep('done')}>
                  Skip for now
                </Button>
                <p className="muted">
                  Without a domain, the panel stays reachable at this IP address. Browsers keep showing a warning about
                  the certificate, which is expected for your own server. You can add a domain later under Settings.
                </p>
              </div>
            ) : null}
          </section>
        ) : (
          <section className="wizard-step" aria-labelledby="wizard-heading">
            <h2 id="wizard-heading" ref={headingRef} tabIndex={-1}>
              Almost done
            </h2>
            <p className="muted">Check your choices. You can change them later under Settings.</p>
            <Summary settings={settings} />
            {settings.panel_domain ? (
              <p>After you finish, this IP address sends you to {settings.panel_domain}.</p>
            ) : null}
          </section>
        )}

        {finished ? null : (
          <div className="wizard-actions">
            {hint ? <p className="muted wizard-hint">{hint}</p> : null}
            {step !== 'addresses' ? <Button onClick={() => setStep(previousStep(step))}>Back</Button> : null}
            {step === 'done' ? (
              <Button data-variant="solid" pendingAnnouncement="Finishing" onClick={finish}>
                Finish setup
              </Button>
            ) : (
              <Button data-variant="solid" pendingAnnouncement="Saving" disabled={!allowed} onClick={goOn}>
                Continue
              </Button>
            )}
          </div>
        )}
      </div>
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
