import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'

import { pollInterval, watchPaths } from '@/lib/k8s-watch'

export type LiveState<T> =
  | { phase: 'loading' }
  // status is the HTTP status of the failed read when there was one, so the
  // screen can say "not allowed" rather than "cannot reach" (see load-error.ts).
  | { phase: 'error'; message: string; status?: number }
  // staleError is set when a refresh failed after data had been read: `data` is
  // then the last good read, not the current state of the cluster, and the view
  // must say so rather than drop it or pass it off as fresh.
  | { phase: 'ready'; data: T; staleError?: string; lastRead?: number }

/**
 * What a failed fetch does to the state. Before any data was read it is a plain
 * error; after, the last good data stays on screen and is marked stale, because
 * blanking a screen on one failed poll hides exactly what an operator is
 * watching (an active `noout` flag, an OSD replacement waiting for approval).
 */
export function stateAfterFailure<T>(
  prev: LiveState<T>,
  message: string,
  keepStale: boolean,
  status?: number,
): LiveState<T> {
  if (keepStale && prev.phase === 'ready') return { ...prev, staleError: message }
  return status === undefined ? { phase: 'error', message } : { phase: 'error', message, status }
}

export function stateAfterSuccess<T>(data: T, now: number = Date.now()): LiveState<T> {
  return { phase: 'ready', data, lastRead: now }
}

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
 * does. `pollMs` is floored at 5000ms by `pollInterval` itself; most screens
 * want 30000+, and only something that visibly changes second to second earns
 * a faster rate.
 *
 * Pick one or the other when the fetcher reads only one kind of thing — watch
 * for real objects, poll for a metric. Passing both is legitimate, not a
 * "just in case," for a fetcher that genuinely reads both: `nodes()` and
 * `capacity()` return real Node/Pod state (readiness, roles, allocatable)
 * *and* metrics-server usage layered on top, and metrics-server has no watch
 * support of its own. Watching alone would leave the usage numbers frozen
 * between object-level events, which on a quiet cluster can be a long time —
 * exactly the staleness this hook exists to fix. In that case keep the watch
 * for the object half and add a *slow* poll (30000ms+) for the metric half:
 * slow, because the watch already catches everything that changes discretely,
 * so the poll only needs to catch the one thing that drifts continuously.
 */
export function useLiveResource<T>(
  fetcher: () => Promise<T>,
  deps: unknown[] = [],
  watch: string[] = [],
  pollMs?: number,
  options: { keepStaleOnError?: boolean } = {},
): { state: LiveState<T>; reload: () => void } {
  // Opt-in: a view that shows `StaleNotice` can keep its last good read through
  // a failed refresh. A view that does not must still turn into an error, or it
  // would pass old data off as current.
  const keepStale = options.keepStaleOnError === true
  const [state, setState] = useState<LiveState<T>>({ phase: 'loading' })

  // When the caller's deps change the data on screen belongs to the old deps:
  // show the loading state in the same render instead of flashing it for a
  // frame and flipping it from an effect. Storing the previous deps in state
  // and adjusting during render is React's own pattern for this.
  const [seenDeps, setSeenDeps] = useState(deps)
  if (seenDeps.length !== deps.length || seenDeps.some((d, i) => !Object.is(d, deps[i]))) {
    setSeenDeps(deps)
    setState({ phase: 'loading' })
  }

  // The fetcher is rebuilt on every render by most callers, so the effects key
  // off `deps` instead and read the current fetcher through a ref. Without this
  // the watch would be torn down and re-established on each render. The ref is
  // written after the render commits (a layout effect, so before any passive
  // effect or event handler reads it), never during render.
  const fetcherRef = useRef(fetcher)
  useLayoutEffect(() => {
    fetcherRef.current = fetcher
  })

  const cancelledRef = useRef(false)

  // Stable: it reads the current fetcher through the ref, so what makes the
  // resource re-read itself when the caller's `deps` change is the effect
  // below, not this function's identity.
  const requestRef = useRef(0)
  const keepStaleRef = useRef(keepStale)
  useLayoutEffect(() => {
    keepStaleRef.current = keepStale
  })

  const load = useCallback(() => {
    // Only the newest request may write: a slow answer to an older one (watch and
    // poll can overlap) must not overwrite, or mark stale, a newer result.
    const mine = ++requestRef.current
    fetcherRef
      .current()
      .then((data) => {
        if (!cancelledRef.current && mine === requestRef.current) setState(stateAfterSuccess(data))
      })
      .catch((e: unknown) => {
        if (cancelledRef.current || mine !== requestRef.current) return
        const message = e instanceof Error ? e.message : 'Failed to reach the Kubernetes API'
        const status = typeof (e as { statusCode?: unknown })?.statusCode === 'number'
          ? (e as { statusCode: number }).statusCode
          : undefined
        setState((prev) => stateAfterFailure(prev, message, keepStaleRef.current, status))
      })
  }, [])

  // A silent refresh keeps what is on screen; a manual one shows loading.
  const run = useCallback(
    (silent: boolean) => {
      if (!silent) setState({ phase: 'loading' })
      load()
    },
    [load],
  )

  const reload = useCallback(() => run(false), [run])

  useEffect(() => {
    cancelledRef.current = false
    load()
    return () => {
      cancelledRef.current = true
    }
    // The caller's deps decide when to re-read; their number is fixed per call site.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load, ...deps])

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
