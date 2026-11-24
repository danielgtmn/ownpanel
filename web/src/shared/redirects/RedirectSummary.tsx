import type { ReactNode } from 'react'
import type { Redirect } from '../api/index.ts'
import { codeLabel, keepsSummary, redirectFrom } from './rules.ts'

export function RedirectSummary({ redirect, children }: { redirect: Redirect; children?: ReactNode }) {
  const keeps = keepsSummary(redirect)
  return (
    <div className="record-main">
      <h3 className="record-title redirect-title">
        <span className="redirect-from">{redirectFrom(redirect)}</span>
        <span className="redirect-arrow" aria-hidden="true">
          →
        </span>
        <span className="visually-hidden"> leads to </span>
        <span className="redirect-to">{redirect.target}</span>
      </h3>
      <ul className="record-facts">
        <li>{codeLabel(redirect.code)}</li>
        {keeps.map((text) => (
          <li key={text}>{text}</li>
        ))}
      </ul>
      {children}
    </div>
  )
}
