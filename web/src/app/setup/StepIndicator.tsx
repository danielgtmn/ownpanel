import { STEPS, stepNumber, type StepId } from './wizard.ts'

function CheckMark() {
  return (
    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3" aria-hidden="true">
      <path d="M5 12.5l4.5 4.5L19 7.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

export function StepIndicator({ current }: { current: StepId }) {
  const currentNumber = stepNumber(current)
  return (
    <nav className="steps" aria-label="Setup steps">
      <ol className="steps-list">
        {STEPS.map((step, index) => {
          const number = index + 1
          const state = number < currentNumber ? 'done' : number === currentNumber ? 'current' : 'upcoming'
          return (
            <li key={step.id} data-state={state} aria-current={state === 'current' ? 'step' : undefined}>
              <span className="step-marker" aria-hidden="true">
                {state === 'done' ? <CheckMark /> : number}
              </span>
              <span className="step-name">
                {step.name}
                {state === 'done' ? <span className="visually-hidden"> (done)</span> : null}
              </span>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}
