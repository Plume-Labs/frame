import { useCallback, useEffect, useState } from 'react'

export type LiveState<T> =
  | { phase: 'loading' }
  | { phase: 'error'; message: string }
  | { phase: 'ready'; data: T }

/**
 * Fetch a live cluster resource with loading / error / ready states and a
 * refetch. Used by the views that read the real Kubernetes API (nodes, events,
 * applications) instead of the simulator.
 */
export function useLiveResource<T>(
  fetcher: () => Promise<T>,
  deps: unknown[] = [],
): { state: LiveState<T>; reload: () => void } {
  const [state, setState] = useState<LiveState<T>>({ phase: 'loading' })

  const reload = useCallback(() => {
    let cancelled = false
    setState({ phase: 'loading' })
    fetcher()
      .then((data) => {
        if (!cancelled) setState({ phase: 'ready', data })
      })
      .catch((e: unknown) => {
        if (!cancelled)
          setState({
            phase: 'error',
            message: e instanceof Error ? e.message : 'Failed to reach the Kubernetes API',
          })
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  useEffect(() => {
    const cancel = reload()
    return cancel
  }, [reload])

  return { state, reload }
}
