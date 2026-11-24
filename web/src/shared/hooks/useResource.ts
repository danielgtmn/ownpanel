import { useCallback, useEffect, useRef, useState } from 'react'

type State<T> =
  | { key: string; status: 'loading' }
  | { key: string; status: 'ready'; data: T }
  | { key: string; status: 'error'; error: unknown }

export type Resource<T> =
  | { status: 'loading'; reload: () => Promise<void>; retry: () => void }
  | { status: 'ready'; data: T; reload: () => Promise<void>; retry: () => void }
  | { status: 'error'; error: unknown; reload: () => Promise<void>; retry: () => void }

export function useResource<T>(load: () => Promise<T>, key: string): Resource<T> {
  const [state, setState] = useState<State<T>>({ key, status: 'loading' })
  const loadRef = useRef(load)
  const latest = useRef(0)

  useEffect(() => {
    loadRef.current = load
  })

  const run = useCallback(async () => {
    const id = ++latest.current
    try {
      const data = await loadRef.current()
      if (id === latest.current) setState({ key, status: 'ready', data })
    } catch (error) {
      if (id === latest.current) setState({ key, status: 'error', error })
    }
  }, [key])

  useEffect(() => {
    const counter = latest
    void run()
    return () => {
      counter.current++
    }
  }, [run])

  const retry = useCallback(() => {
    setState({ key, status: 'loading' })
    void run()
  }, [key, run])

  if (state.key !== key || state.status === 'loading') {
    return { status: 'loading', reload: run, retry }
  }
  if (state.status === 'error') {
    return { status: 'error', error: state.error, reload: run, retry }
  }
  return { status: 'ready', data: state.data, reload: run, retry }
}
