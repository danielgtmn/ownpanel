import { useEffect, useState } from 'react'
import { getPanelChecks, type PanelChecks } from './api.ts'
import { startPolling } from './poller.ts'
import { checksSettled } from './rules.ts'

export const CHECK_INTERVAL = 5000

export type ChecksState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ready'; checks: PanelChecks }
  | { status: 'error'; error: unknown; checks: PanelChecks | null }

function lastChecks(state: ChecksState): PanelChecks | null {
  return state.status === 'ready' || state.status === 'error' ? state.checks : null
}

// Polls while `key` is non-empty (the panel domain and the addresses it should point to); a new key starts over.
export function usePanelChecks(key: string): ChecksState {
  const [state, setState] = useState<{ key: string; value: ChecksState }>({ key: '', value: { status: 'idle' } })

  useEffect(() => {
    if (!key) return
    return startPolling({
      load: getPanelChecks,
      interval: CHECK_INTERVAL,
      isDone: checksSettled,
      onResult: (checks) => setState({ key, value: { status: 'ready', checks } }),
      onError: (error) =>
        setState((current) => ({
          key,
          value: { status: 'error', error, checks: current.key === key ? lastChecks(current.value) : null },
        })),
    })
  }, [key])

  if (!key) return { status: 'idle' }
  if (state.key !== key) return { status: 'loading' }
  return state.value
}
