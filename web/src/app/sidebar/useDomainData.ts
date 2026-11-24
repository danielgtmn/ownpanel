import { useEffect, useMemo, useState } from 'react'
import { onDataChange } from '../../shared/api/index.ts'
import { buildFamilies, type DomainSources, type Family, type PointStates } from '../../shared/families/index.ts'
import { useResource } from '../../shared/hooks/index.ts'
import { loadPointStates, loadSources } from './data.ts'

export type DomainData =
  | { status: 'loading' }
  | { status: 'error'; retry: () => void }
  | { status: 'ready'; sources: DomainSources; families: Family[]; states: PointStates }

export function useDomainData(isAdmin: boolean): DomainData {
  const resource = useResource(() => loadSources(isAdmin), `domain-sources:${isAdmin}`)
  const { reload } = resource
  const sources = resource.status === 'ready' ? resource.data : null
  const [states, setStates] = useState<PointStates>({})

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const soon = () => {
      clearTimeout(timer)
      timer = setTimeout(() => void reload(), 400)
    }
    const onVisible = () => {
      if (document.visibilityState === 'visible') soon()
    }
    const stop = onDataChange(soon)
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      clearTimeout(timer)
      stop()
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [reload])

  useEffect(() => {
    if (!sources) return
    let current = true
    void loadPointStates(sources, isAdmin).then((next) => {
      if (current) setStates(next)
    })
    return () => {
      current = false
    }
  }, [sources, isAdmin])

  const families = useMemo(() => (sources ? buildFamilies(sources) : []), [sources])
  if (sources) return { status: 'ready', sources, families, states }
  if (resource.status === 'error') return { status: 'error', retry: resource.retry }
  return { status: 'loading' }
}
