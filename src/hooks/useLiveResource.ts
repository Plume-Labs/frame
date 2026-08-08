import { useCallback, useEffect, useRef, useState } from 'react'

import { pollInterval, watchPaths } from '@/lib/k8s-watch'

export type LiveState<T> =
  | { phase: 'loading' }
  | { phase: 'error'; message: string }
  | { phase: 'ready'; data: T }

/**
 * Fetch a live cluster resource with loading / error / ready states and a
 * refetch. Used by the views that read the real Kubernetes API (nodes, events,
 * applications) instead of the simulator.
 *
 * Pass `watch` a list of Kubernetes list paths and the resource re-reads itself
 * whenever the apiserver reports one of them changed, so a job that finishes
 * shows as finished without anyone reloading the page. Those refreshes are
 * silent: the view keeps rendering the data it has rather than flashing its
 * loading skeleton on every event.
 *
 * Pass `pollMs` instead for a fetcher with nothing to watch — a Prometheus
 * query, a DCGM/node-exporter scrape, or anything else read through
 * `integrationProxy`. A metric is not a Kubernetes object, so the apiserver
 * has no change stream for it. Polling refreshes the same silent way watching
 * does. A resource should get one mechanism or the other, never both — pick
 * `watch` when the fetcher reads real objects and `pollMs` when it reads a
 * metric, and keep `pollMs` at 5000 or above (most screens want 30000+; only
 * something that visibly changes second to second earns a faster rate).
 */
export function useLiveResource<T>(
  fetcher: () => Promise<T>,
  deps: unknown[] = [],
  watch: string[] = [],
  pollMs?: number,
): { state: LiveState<T>; reload: () => void } {
  const [state, setState] = useState<LiveState<T>>({ phase: 'loading' })

  // The fetcher is rebuilt on every render by most callers, so the effects key
  // off `deps` instead and read the current fetcher through a ref. Without this
  // the watch would be torn down and re-established on each render.
  const fetcherRef = useRef(fetcher)
  fetcherRef.current = fetcher

  const cancelledRef = useRef(false)

  const run = useCallback(
    (silent: boolean) => {
      if (!silent) setState({ phase: 'loading' })
      fetcherRef
        .current()
        .then((data) => {
          if (!cancelledRef.current) setState({ phase: 'ready', data })
        })
        .catch((e: unknown) => {
          if (cancelledRef.current) return
          setState({
            phase: 'error',
            message: e instanceof Error ? e.message : 'Failed to reach the Kubernetes API',
          })
        })
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    deps,
  )

  const reload = useCallback(() => run(false), [run])

  useEffect(() => {
    cancelledRef.current = false
    run(false)
    return () => {
      cancelledRef.current = true
    }
  }, [run])

  // Joined so a caller can build the array inline without re-subscribing on
  // every render.
  const watchKey = watch.join('|')

  useEffect(() => {
    if (!watchKey) return
    return watchPaths(watchKey.split('|'), () => run(true))
  }, [watchKey, run])

  useEffect(() => {
    if (!pollMs) return
    return pollInterval(pollMs, () => run(true))
  }, [pollMs, run])

  return { state, reload }
}
