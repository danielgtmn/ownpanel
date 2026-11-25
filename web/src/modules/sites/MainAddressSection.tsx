import { confirm } from '@ui-sdk/react/alert-dialog'
import { Button } from '@ui-sdk/react/button'
import { DialogClose } from '@ui-sdk/react/dialog'
import { Field, FieldDescription, FieldLabel } from '@ui-sdk/react/field'
import { Radio, RadioGroup, RadioIndicator } from '@ui-sdk/react/radio'
import { toast } from '@ui-sdk/react/toast'
import { useState } from 'react'
import { errorMessage, type Redirect } from '../../shared/api/index.ts'
import { DomainCheck } from '../../shared/domains/index.ts'
import type { Resource } from '../../shared/hooks/index.ts'
import { ErrorState, FormDialog, LoadingState, Section } from '../../shared/ui/index.ts'
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
  type CurrentSetup,
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

type ChooserProps = Omit<PairProps, 'showHeading'> & { onDone: () => void }

function PairChooser({ site, pair, redirects, pathRedirects, result, onResult, onChanged, onDone }: ChooserProps) {
  const state = pairState(pair, site.domains, redirects)
  const current = currentSetup(state)
  const currentChoice = current.kind === 'choice' ? current.choice : null
  const [choice, setChoice] = useState<MainChoice | null>(currentChoice)
  const steps = choice ? planMainAddress(state, choice) : []
  const lost = pathRedirectsLost(steps, pathRedirects)
  const keep = mainWithoutRedirect(state)
  const keepOther = keep === pair.bare ? pair.www : pair.bare

  async function apply() {
    if (steps.length === 0) {
      onDone()
      return
    }
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
      toast.success('Main address saved.')
      onDone()
    }
    await onChanged()
  }

  return (
    <form className="form" onSubmit={(event) => event.preventDefault()} noValidate>
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
            setChoice(value as MainChoice)
            onResult(null)
          }}
        >
          <Field>
            <Radio value="bare">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>{pair.bare}</FieldLabel>
              <FieldDescription>Visitors of {pair.www} are sent here.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="www">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>{pair.www}</FieldLabel>
              <FieldDescription>Visitors of {pair.bare} are sent here.</FieldDescription>
            </div>
          </Field>
          <Field>
            <Radio value="none">
              <RadioIndicator />
            </Radio>
            <div className="choice-text">
              <FieldLabel>Only {keep}</FieldLabel>
              <FieldDescription>{keepOther} isn't used.</FieldDescription>
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
        </div>
      ) : null}
      {result?.failed ? <ResultNotice result={result} failed={result.failed} siteName={site.name} /> : null}
      <div className="form-actions">
        <DialogClose>Cancel</DialogClose>
        <Button type="submit" data-variant="solid" pendingAnnouncement="Saving" onClick={apply}>
          Save main address
        </Button>
      </div>
    </form>
  )
}

function summary(current: CurrentSetup, pair: AddressPair): string {
  if (current.kind === 'both') return `Both ${pair.bare} and ${pair.www} show this site.`
  if (current.kind === 'elsewhere') return `${current.main} shows this site. ${current.other} redirects to ${current.target}.`
  if (current.choice === 'none') return `Only ${current.main} shows this site. ${current.other} isn't used.`
  return `${current.main} is the main address. Visitors of ${current.other} are sent there.`
}

function PairRow({ site, pair, redirects, pathRedirects, showHeading, result, onResult, onChanged }: PairProps) {
  const [open, setOpen] = useState(false)
  const current = currentSetup(pairState(pair, site.domains, redirects))
  const redirectDomain = current.kind === 'choice' && current.choice !== 'none' ? current.other : null

  return (
    <div className="main-address">
      <div className="main-address-row">
        <div className="choice-text">
          {showHeading ? <h3 className="main-address-heading">{pair.bare}</h3> : null}
          <p className="main-address-now">{summary(current, pair)}</p>
        </div>
        <Button onClick={() => setOpen(true)}>
          Change<span className="visually-hidden"> main address for {pair.bare}</span>
        </Button>
      </div>
      {!open && result?.failed ? <ResultNotice result={result} failed={result.failed} siteName={site.name} /> : null}
      {redirectDomain ? (
        <div className="main-address-dns">
          <p>
            <strong>{redirectDomain}</strong> redirects to {current.kind === 'choice' ? current.main : ''}. It needs to
            point to this server too:
          </p>
          <DomainCheck domain={redirectDomain} />
        </div>
      ) : null}
      <FormDialog
        open={open}
        onOpenChange={setOpen}
        title="Main address"
        description="Choose the address visitors end up on. The other one sends them there and keeps the rest of the address."
      >
        <PairChooser
          site={site}
          pair={pair}
          redirects={redirects}
          pathRedirects={pathRedirects}
          result={result}
          onResult={onResult}
          onChanged={onChanged}
          onDone={() => setOpen(false)}
        />
      </FormDialog>
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
      description="The address visitors end up on."
    >
      {redirects.status === 'loading' ? <LoadingState label="Loading redirects…" /> : null}
      {redirects.status === 'error' ? (
        <ErrorState title="Couldn't load the redirects" message={errorMessage(redirects.error)} onRetry={redirects.retry} />
      ) : null}
      {redirects.status === 'ready'
        ? pairs.map((pair) => (
            <PairRow
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
