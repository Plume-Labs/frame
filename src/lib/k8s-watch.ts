/**
 * Watch streams against the Kubernetes API.
 *
 * The apiserver the UI already talks to serves `?watch=true` as a chunked
 * stream of newline-delimited JSON events, so live updates need no server of
 * our own — only a reader on this side.
 *
 * ponytail: a watch here is a *change signal*, not a data source. When an event
 * arrives the caller re-runs the fetch it would have run anyway, rather than
 * applying the event to a local cache. That keeps every view's existing
 * mapping code untouched, at the cost of one extra list per change. At this
 * cluster's scale that is free; if a screen ever watches something that churns
 * per second, give that screen an incremental cache instead.
 */

/** How long to coalesce a burst of events before firing the callback. */
const COALESCE_MS = 250

/** Reconnect backoff, doubling from the first to the cap. */
const BACKOFF_START_MS = 1_000
const BACKOFF_MAX_MS = 30_000

/**
 * After this many consecutive failures to establish a watch, stop trying and
 * fall back to an interval. Watches can be unavailable for reasons we cannot
 * fix from here — an intermediary that buffers chunked responses, RBAC without
 * the `watch` verb — and a screen that silently stops updating is worse than
 * one that updates slowly.
 */
const FALLBACK_AFTER_FAILURES = 3
const FALLBACK_POLL_MS = 15_000

type EventType = 'ADDED' | 'MODIFIED' | 'DELETED' | 'BOOKMARK' | 'ERROR'

interface WatchEvent {
  type: EventType
  object?: {
    kind?: string
    code?: number
    metadata?: { resourceVersion?: string }
  }
}

interface ListResponse {
  metadata?: { resourceVersion?: string }
}

function authHeaders(): Record<string, string> {
  if (typeof window === 'undefined') return {}
  const token = (window as unknown as Record<string, string>).__FRAME_TOKEN__
  return token ? { Authorization: `Bearer ${token}` } : {}
}

/**
 * Watch several list paths and call `onChange` when any of them changes.
 *
 * Each path is a Kubernetes list endpoint, e.g.
 * `/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs` or
 * `/api/v1/nodes`. Returns a function that stops every stream it started.
 */
export function watchPaths(paths: string[], onChange: () => void): () => void {
  const controller = new AbortController()
  let coalesceTimer: ReturnType<typeof setTimeout> | undefined

  const signalChange = () => {
    if (coalesceTimer !== undefined) return
    coalesceTimer = setTimeout(() => {
      coalesceTimer = undefined
      if (!controller.signal.aborted) onChange()
    }, COALESCE_MS)
  }

  for (const path of paths) {
    void runWatchLoop(path, signalChange, controller.signal)
  }

  return () => {
    controller.abort()
    if (coalesceTimer !== undefined) clearTimeout(coalesceTimer)
  }
}

/**
 * Keep one path watched until aborted: list to get a resourceVersion, stream
 * from it, and start over when the stream ends. A watch ending is routine —
 * the apiserver closes them on its own timeout — so a clean end reconnects at
 * once and only errors back off.
 */
async function runWatchLoop(
  path: string,
  onChange: () => void,
  signal: AbortSignal,
): Promise<void> {
  let backoff = BACKOFF_START_MS
  let failures = 0

  while (!signal.aborted) {
    try {
      const resourceVersion = await listResourceVersion(path, signal)
      if (signal.aborted) return
      failures = 0
      backoff = BACKOFF_START_MS

      await streamEvents(path, resourceVersion, onChange, signal)
      // A clean end means the apiserver timed the watch out. Re-list and go
      // again immediately: waiting here would be a gap in liveness for a
      // condition that is not an error.
      continue
    } catch (err) {
      if (signal.aborted) return

      // 410 Gone: the resourceVersion aged out of the apiserver's window. The
      // fix is a fresh list, which the next iteration does anyway, so this is
      // not a failure and must not count towards the fallback.
      if (isExpiredResourceVersion(err)) continue

      failures += 1
      if (failures >= FALLBACK_AFTER_FAILURES) {
        pollUntilAborted(onChange, signal)
        return
      }

      await sleep(backoff, signal)
      backoff = Math.min(backoff * 2, BACKOFF_MAX_MS)
    }
  }
}

/** List once, only to learn the resourceVersion the watch should start from. */
async function listResourceVersion(path: string, signal: AbortSignal): Promise<string> {
  const res = await fetch(`${path}?limit=1`, { headers: authHeaders(), signal })
  if (!res.ok) throw new WatchError(res.status, `Listing ${path} failed: ${res.statusText}`)
  const body = (await res.json()) as ListResponse
  const rv = body.metadata?.resourceVersion
  if (!rv) throw new WatchError(0, `Listing ${path} returned no resourceVersion`)
  return rv
}

/**
 * Read one watch stream to its end, calling `onChange` per meaningful event.
 * Resolves when the apiserver closes the stream; rejects on transport failure
 * or on an ERROR event the stream reports in-band.
 */
async function streamEvents(
  path: string,
  resourceVersion: string,
  onChange: () => void,
  signal: AbortSignal,
): Promise<void> {
  const url = `${path}?watch=true&allowWatchBookmarks=true&resourceVersion=${encodeURIComponent(resourceVersion)}`
  const res = await fetch(url, { headers: authHeaders(), signal })
  if (!res.ok) throw new WatchError(res.status, `Watching ${path} failed: ${res.statusText}`)
  if (!res.body) throw new WatchError(0, `Watching ${path} returned no stream`)

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) return
      buffer += decoder.decode(value, { stream: true })

      // Events are newline-delimited, and a chunk can split one in half, so
      // the trailing partial line stays in the buffer for the next chunk.
      const lines = buffer.split('\n')
      buffer = lines.pop() ?? ''

      for (const line of lines) {
        if (!line.trim()) continue
        const event = parseEvent(line)
        if (!event) continue
        if (event.type === 'ERROR') {
          throw new WatchError(event.object?.code ?? 0, `Watch on ${path} reported an error`)
        }
        // A BOOKMARK carries only a resourceVersion, never a change. Firing on
        // it would refetch on the apiserver's heartbeat.
        if (event.type === 'BOOKMARK') continue
        onChange()
      }
    }
  } finally {
    // Releasing the lock lets the abort actually cancel the body; without it a
    // stopped watch keeps its connection open until the server gives up.
    reader.releaseLock()
  }
}

function parseEvent(line: string): WatchEvent | undefined {
  try {
    return JSON.parse(line) as WatchEvent
  } catch {
    // A malformed line is not worth tearing the stream down for: the next one
    // is very likely fine, and the caller refetches whole state anyway.
    return undefined
  }
}

/** Last resort when a watch cannot be established at all. */
function pollUntilAborted(onChange: () => void, signal: AbortSignal): void {
  const timer = setInterval(() => {
    if (signal.aborted) {
      clearInterval(timer)
      return
    }
    onChange()
  }, FALLBACK_POLL_MS)
  signal.addEventListener('abort', () => clearInterval(timer), { once: true })
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    signal.addEventListener(
      'abort',
      () => {
        clearTimeout(timer)
        resolve()
      },
      { once: true },
    )
  })
}

/**
 * Nothing here should ever refresh faster than this — see `useLiveResource`'s
 * doc comment for why. Enforced here, not just documented, because every call
 * site today is a literal above the floor and "trust the convention" is
 * exactly the kind of thing a later edit quietly violates.
 */
const MIN_POLL_MS = 5_000

/**
 * Poll on a fixed interval until stopped.
 *
 * For resources with nothing to watch — a Prometheus query, a DCGM/node-exporter
 * scrape, or anything else read through `integrationProxy`. A metric is not a
 * Kubernetes object, so the apiserver has no change stream for it; polling is
 * the only option.
 *
 * Mirrors `watchPaths`' shape (a start call returning its own stop function) so
 * `useLiveResource` can wire either one into the same "run a silent refresh"
 * callback. Never fires immediately — the first call lands after the first
 * full interval — so subscribing never causes a surprise extra refresh on top
 * of the initial (non-silent) fetch a screen already did on mount.
 *
 * `ms` below `MIN_POLL_MS` is clamped rather than rejected: a screen with a
 * too-aggressive interval should degrade to "slower than asked," not crash.
 */
export function pollInterval(ms: number, onChange: () => void): () => void {
  const timer = setInterval(onChange, Math.max(ms, MIN_POLL_MS))
  return () => clearInterval(timer)
}

export class WatchError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
    this.name = 'WatchError'
  }
}

function isExpiredResourceVersion(err: unknown): boolean {
  return err instanceof WatchError && err.status === 410
}
