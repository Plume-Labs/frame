import { describe, it, expect, vi, afterEach } from 'vitest'

import { pollInterval, watchPaths } from './k8s-watch'

const PATH = '/apis/frame.plume-labs.io/v1beta1/namespaces/frame-system/framejobs'

/** A response body that emits the given chunks, then ends the stream. */
function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  let i = 0
  return new ReadableStream({
    pull(controller) {
      if (i >= chunks.length) {
        controller.close()
        return
      }
      controller.enqueue(encoder.encode(chunks[i]))
      i += 1
    },
  })
}

function listResponse(resourceVersion = '42'): Response {
  return new Response(JSON.stringify({ metadata: { resourceVersion } }), { status: 200 })
}

function watchResponse(chunks: string[]): Response {
  return new Response(streamOf(chunks), { status: 200 })
}

/**
 * Answer the list, then the watch, then hang so the loop cannot spin: the loop
 * re-lists as soon as a stream ends, which without this would race the test.
 */
function fetchOnce(chunks: string[]): typeof fetch {
  let call = 0
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    call += 1
    if (call === 1) return listResponse()
    if (call === 2) {
      expect(url).toContain('watch=true')
      expect(url).toContain('resourceVersion=42')
      return watchResponse(chunks)
    }
    return new Promise<Response>(() => {})
  }) as unknown as typeof fetch
}

const event = (type: string) =>
  JSON.stringify({ type, object: { metadata: { resourceVersion: '43' } } }) + '\n'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

/** Let the watch loop's pending microtasks and stream reads settle. */
async function settle() {
  for (let i = 0; i < 20; i++) await Promise.resolve()
  await new Promise((r) => setTimeout(r, 0))
  for (let i = 0; i < 20; i++) await Promise.resolve()
}

describe('watchPaths', () => {
  it('fires once for a change, after the coalescing window', async () => {
    vi.stubGlobal('fetch', fetchOnce([event('MODIFIED')]))
    const onChange = vi.fn()

    const stop = watchPaths([PATH], onChange)
    await settle()

    // Still inside the coalescing window: nothing yet.
    expect(onChange).not.toHaveBeenCalled()

    await new Promise((r) => setTimeout(r, 300))
    expect(onChange).toHaveBeenCalledTimes(1)
    stop()
  })

  it('coalesces a burst into a single refetch', async () => {
    vi.stubGlobal(
      'fetch',
      fetchOnce([event('ADDED'), event('MODIFIED'), event('MODIFIED'), event('DELETED')]),
    )
    const onChange = vi.fn()

    const stop = watchPaths([PATH], onChange)
    await settle()
    await new Promise((r) => setTimeout(r, 300))

    // Four events, one refetch — the point of coalescing.
    expect(onChange).toHaveBeenCalledTimes(1)
    stop()
  })

  it('ignores bookmarks, which carry no change', async () => {
    vi.stubGlobal('fetch', fetchOnce([event('BOOKMARK')]))
    const onChange = vi.fn()

    const stop = watchPaths([PATH], onChange)
    await settle()
    await new Promise((r) => setTimeout(r, 300))

    expect(onChange).not.toHaveBeenCalled()
    stop()
  })

  it('reassembles an event split across chunks', async () => {
    const whole = event('MODIFIED')
    const cut = Math.floor(whole.length / 2)
    vi.stubGlobal('fetch', fetchOnce([whole.slice(0, cut), whole.slice(cut)]))
    const onChange = vi.fn()

    const stop = watchPaths([PATH], onChange)
    await settle()
    await new Promise((r) => setTimeout(r, 300))

    // A chunk boundary inside a JSON line must not lose or duplicate the event.
    expect(onChange).toHaveBeenCalledTimes(1)
    stop()
  })

  it('stops calling back once stopped, even for events already in flight', async () => {
    vi.stubGlobal('fetch', fetchOnce([event('MODIFIED')]))
    const onChange = vi.fn()

    const stop = watchPaths([PATH], onChange)
    await settle()
    stop()

    await new Promise((r) => setTimeout(r, 300))
    expect(onChange).not.toHaveBeenCalled()
  })

  it('gives up on the watch and falls back to an interval', async () => {
    // Every list fails, so the loop can never establish a watch.
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('nope', { status: 500 })) as unknown as typeof fetch,
    )
    const onChange = vi.fn()

    const stop = watchPaths([PATH], onChange)
    // Three failures at 1s and 2s of backoff, then the fallback interval at 15s.
    await vi.waitFor(() => expect(onChange).toHaveBeenCalled(), { timeout: 25_000 })
    stop()
  }, 30_000)
})

describe('pollInterval', () => {
  // The 5s floor makes a real-time wait impractically slow for a unit test,
  // so these three drive fake timers instead — the one deliberate exception
  // to this file's real-timer style. `afterEach` above already restores real
  // timers, so nothing extra to clean up.

  it('fires repeatedly on the given interval', () => {
    vi.useFakeTimers()
    const onChange = vi.fn()
    const stop = pollInterval(5_000, onChange)

    vi.advanceTimersByTime(4_999)
    expect(onChange).not.toHaveBeenCalled()

    vi.advanceTimersByTime(1)
    expect(onChange).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(15_000)
    expect(onChange).toHaveBeenCalledTimes(4)

    stop()
  })

  it('never fires before the first interval elapses', () => {
    vi.useFakeTimers()
    const onChange = vi.fn()
    const stop = pollInterval(30_000, onChange)

    vi.advanceTimersByTime(29_999)
    stop()

    // Mirrors watchPaths, which never calls back synchronously on subscribe
    // either: starting a poll must not itself produce an extra refresh on top
    // of the initial fetch a screen already did on mount. This is a distinct
    // property from "a refresh does not flash the loading skeleton" — that
    // one lives in useLiveResource's `run(true)` path and is covered in
    // useLiveResource.test.ts, not here.
    expect(onChange).not.toHaveBeenCalled()
  })

  it('stops firing once stopped, even for a tick already scheduled', () => {
    vi.useFakeTimers()
    const onChange = vi.fn()
    const stop = pollInterval(5_000, onChange)

    vi.advanceTimersByTime(5_000)
    expect(onChange).toHaveBeenCalledTimes(1)

    stop()
    vi.advanceTimersByTime(20_000)
    expect(onChange).toHaveBeenCalledTimes(1)
  })

  it('clamps an interval below the 5s floor instead of honouring it', () => {
    // globalThis, not global: `tsc -b` type-checks this file as part of
    // `npm run build`, and the browser lib config it runs under has no Node
    // `global`. The image build failed on TS2304 with the runtime test passing
    // — a green test suite is not evidence that the app compiles.
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval')
    const stop = pollInterval(100, () => {})

    expect(setIntervalSpy).toHaveBeenCalledWith(expect.any(Function), 5_000)
    stop()
  })
})
