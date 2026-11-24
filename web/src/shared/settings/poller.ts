export type PollerOptions<T> = {
  load: (signal: AbortSignal) => Promise<T>
  interval: number
  onResult: (value: T) => void
  onError: (error: unknown) => void
  isDone: (value: T) => boolean
}

// Loads at once, then again `interval` ms after each answer, until isDone or stop().
export function startPolling<T>({ load, interval, onResult, onError, isDone }: PollerOptions<T>): () => void {
  const controller = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined

  async function tick() {
    timer = undefined
    let done = false
    try {
      const value = await load(controller.signal)
      if (controller.signal.aborted) return
      onResult(value)
      done = isDone(value)
    } catch (error) {
      if (controller.signal.aborted) return
      onError(error)
    }
    if (!done) timer = setTimeout(tick, interval)
  }

  void tick()
  return () => {
    controller.abort()
    if (timer !== undefined) clearTimeout(timer)
  }
}
