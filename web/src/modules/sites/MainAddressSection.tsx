import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage, type Redirect } from '../../shared/api/index.ts'
import { DomainCheck } from '../../shared/domains/index.ts'
import type { Resource } from '../../shared/hooks/index.ts'
import { ErrorState, LoadingState, Section } from '../../shared/ui/index.ts'
import {
  addressPairs,
  currentSetup,
  describeStep,
  mainWithoutRedirect,
  pairState,
  pathRedirectsLost,
  planMainAddress,
  runPlan,
  type AddressPair,
  type MainChoice,
  type PlanResult,
} from './mainAddress.ts'
import type { Site } from './types.ts'

type PairProps = {
  site: Site
  pair: AddressPair
  redirects: Redirect[]
  pathRedirects: Redirect[]
  showHeading: boolean
  result: PlanResult | null
  onResult: (result: PlanResult | null) => void
  onChanged: () => Promise<void>
}

type Failed = NonNullable<PlanResult['failed']>

function ResultNotice({ result, failed, siteName }: { result: PlanResult; failed: Failed; siteName: string }) {
  return (
    <div className="notice main-address-result" role="alert">
      <p>Not everything could be changed. {failed.message}</p>
      {result.done.length > 0 ? (
        <>
          <p>Done:</p>
          <ul>
            {result.done.map((step) => (
              <li key={describeStep(step, siteName)}>{describeStep(step, siteName)}</li>
            ))}
          </ul>
        </>
      ) : null}
      <p>Not done:</p>
      <ul>
        {[failed.step, ...result.remaining].map((step) => (
          <li key={describeStep(step, siteName)}>{describeStep(step, siteName)}</li>
        ))}
      </ul>
    </div>
  )
}

function PairChooser({ site, pair, redirects, pathRedirects, showHeading, result, onResult, onChanged }: PairProps) {
  const state = pairState(pair, site.domains, redirects)
  const current = currentSetup(state)
  const currentChoice = current.kind === 'choice' ? current.choice : null
  const [picked, setPicked] = useState<MainChoice | null>(null)
  const choice = picked ?? currentChoice
  const steps = choice ? planMainAddress(state, choice) : []
  const lost = pathRedirectsLost(steps, pathRedirects)
  const keep = mainWithoutRedirect(state)
  const keepOther = keep === pair.bare ? pair.www : pair.bare

  async function apply() {
    if (lost > 0) {
      const yes = await confirm({
        title: lost === 1 ? 'Remove 1 path redirect?' : `Remove ${lost} path redirects?`,
        description: `Path redirects on a domain that's taken off ${site.name} are removed with it.`,
        confirmLabel: 'Continue',
        cancelLabel: 'Cancel',
      })
      if (!yes) return
    }
    const outcome = await runPlan(site.name, steps)
    for (const warning of outcome.warnings) toast.warning(warning)
    if (outcome.failed) {
      onResult(outcome)
    } else {
      onResult(null)
      setPicked(null)
      toast.success('Main address saved.')
    }
    await onChanged()
  }

  const redirectDomain = current.kind === 'choice' && current.choice !== 'none' ? current.other : null

  return (
    <div className="main-address">
      {showHeading ? <h3 className="main-address-heading">{pair.bare}</h3> : null}
      {current.kind === 'both' ? (
        <p className="muted main-address-now">Right now both {pair.bare} and {pair.www} show this site.</p>
      ) : null}
      {current.kind === 'elsewhere' ? (
        <p className="muted main-address-now">
          Right now {current.other} redirects to {current.target}.
        </p>
      ) : null}
      <Field>
        <FieldLabel className="visually-hidden">Main address for {pair.bare}</FieldLabel>
        <RadioGroup
          value={choice}
          onValueChange={(value) => {
            if (!value) return
            setPicked(value as MainChoice)
            onResult(null)
          }}
        >
          <Field>
            <Radio value="bare">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Use {pair.bare} (redirect www)</FieldLabel>
              <FieldDescription>Visitors of {pair.www} are sent to {pair.bare}.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="www">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>
                Use {pair.www} (redirect {pair.bare})
              </FieldLabel>
              <FieldDescription>Visitors of {pair.bare} are sent to {pair.www}.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="none">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Don't use the other address</FieldLabel>
              <FieldDescription>
                Only {keep} shows this site. {keepOther} isn't used.
              </FieldDescription>
            </div>
          </Field>
        </RadioGroup>
      </Field>
      {steps.length > 0 ? (
        <div className="main-address-plan">
          <p>This will:</p>
          <ol>
            {steps.map((step) => (
              <li key={describeStep(step, site.name)}>{describeStep(step, site.name)}</li>
            ))}
          </ol>
          {lost > 0 ? (
            <p className="notice">
              {lost === 1 ? '1 path redirect' : `${lost} path redirects`} on the removed domain will be removed too.
            </p>
          ) : null}
          <div className="main-address-actions">
            <Button data-variant="solid" pendingAnnouncement="Saving" onClick={apply}>
              Apply<span className="visually-hidden"> main address for {pair.bare}</span>
            </Button>
            {picked ? (
              <Button
                onClick={() => {
                  setPicked(null)
                  onResult(null)
                }}
              >
                Cancel<span className="visually-hidden"> change for {pair.bare}</span>
              </Button>
            ) : null}
          </div>
        </div>
      ) : null}
      {result?.failed ? <ResultNotice result={result} failed={result.failed} siteName={site.name} /> : null}
      {redirectDomain && !picked ? (
        <div className="main-address-dns">
          <p>
            <strong>{redirectDomain}</strong> redirects to {current.kind === 'choice' ? current.main : ''}. It needs to
            point to this server too:
          </p>
          <DomainCheck domain={redirectDomain} />
        </div>
      ) : null}
    </div>
  )
}

type Props = {
  site: Site
  redirects: Resource<Redirect[]>
  pathRedirects: Redirect[]
  onChanged: () => Promise<void>
}

export function MainAddressSection({ site, redirects, pathRedirects, onChanged }: Props) {
  const [results, setResults] = useState<Record<string, PlanResult | null>>({})
  const pairs = addressPairs(site.domains)
  if (pairs.length === 0) return null
  return (
    <Section
      title="Main address"
      description="Choose the address visitors end up on. The other one sends them there and keeps the rest of the address."
    >
      {redirects.status === 'loading' ? <LoadingState label="Loading redirects…" /> : null}
      {redirects.status === 'error' ? (
        <ErrorState title="Couldn't load the redirects" message={errorMessage(redirects.error)} onRetry={redirects.retry} />
      ) : null}
      {redirects.status === 'ready'
        ? pairs.map((pair) => (
            <PairChooser
              key={pair.bare}
              site={site}
              pair={pair}
              redirects={redirects.data}
              pathRedirects={pathRedirects}
              showHeading={pairs.length > 1}
              result={results[pair.bare] ?? null}
              onResult={(result) => setResults((current) => ({ ...current, [pair.bare]: result }))}
              onChanged={onChanged}
            />
          ))
        : null}
    </Section>
  )
}
