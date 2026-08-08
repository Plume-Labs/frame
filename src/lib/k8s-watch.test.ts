import { describe, it, expect, vi, afterEach } from 'vitest'

import { pollInterval, watchPaths } from './k8s-watch'

const PATH = '/apis/frame.plume-labs.io/v1alpha1/namespaces/frame-system/framejobs'

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
  it('fires repeatedly on the given interval', async () => {
    const onChange = vi.fn()
    const stop = pollInterval(20, onChange)
    await new Promise((r) => setTimeout(r, 90))
    stop()

    // ~4 ticks in 90ms at 20ms each; allow slack for CI jitter.
    expect(onChange.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('never fires before the first interval elapses', async () => {
    const onChange = vi.fn()
    const stop = pollInterval(1_000, onChange)
    await new Promise((r) => setTimeout(r, 50))
    stop()

    // The silent contract polling shares with watchPaths: subscribing must not
    // itself trigger a refresh on top of the initial (non-silent) fetch a
    // screen already did on mount — that would flash the loading skeleton a
    // second time for nothing.
    expect(onChange).not.toHaveBeenCalled()
  })

  it('stops firing once stopped, even for a tick already scheduled', async () => {
    const onChange = vi.fn()
    const stop = pollInterval(20, onChange)
    await new Promise((r) => setTimeout(r, 30))
    stop()
    const callsAtStop = onChange.mock.calls.length
    expect(callsAtStop).toBeGreaterThan(0)

    await new Promise((r) => setTimeout(r, 80))
    expect(onChange.mock.calls.length).toBe(callsAtStop)
  })
})
