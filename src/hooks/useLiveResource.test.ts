// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import type { LiveState } from './useLiveResource'
import { useLiveResource } from './useLiveResource'

// React's act() only does its extra flushing/checks when it knows it is
// running in a test environment.
;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// useLiveResource drives watchPaths/pollInterval itself; mocked here so a
// test can fire a "watch changed" or "poll ticked" event on demand instead of
// running a real fetch stream or waiting out a real interval.
const watchCallbacks: Array<() => void> = []
const pollCallbacks: Array<() => void> = []

vi.mock('@/lib/k8s-watch', () => ({
  watchPaths: vi.fn((_paths: string[], onChange: () => void) => {
    watchCallbacks.push(onChange)
    return () => {}
  }),
  pollInterval: vi.fn((_ms: number, onChange: () => void) => {
    pollCallbacks.push(onChange)
    return () => {}
  }),
}))

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  watchCallbacks.length = 0
  pollCallbacks.length = 0
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  vi.restoreAllMocks()
})

function last<T>(arr: T[]): T {
  return arr[arr.length - 1]
}

/**
 * Run `fn`, then give pending promises a real tick to resolve before letting
 * `act` flush whatever state update results. `useLiveResource`'s fetch chain
 * resolves via microtasks with nothing else to await on, so a bare `act(fn)`
 * would return before the resulting `setState` ever ran.
 */
async function step(fn: () => void) {
  await act(async () => {
    fn()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function Harness({
  onState,
  fetcher,
  watch,
  pollMs,
}: {
  onState: (s: LiveState<number>) => void
  fetcher: () => Promise<number>
  watch?: string[]
  pollMs?: number
}) {
  const { state } = useLiveResource<number>(fetcher, [], watch ?? [], pollMs)
  onState(state)
  return null
}

describe('useLiveResource', () => {
  it('a watch event and a poll tick both refresh without ever flashing loading again', async () => {
    let n = 0
    const fetcher = vi.fn(async () => n++)
    const history: LiveState<number>[] = []

    await step(() => {
      root.render(
        createElement(Harness, {
          onState: (s) => history.push(s),
          fetcher,
          watch: ['/api/v1/nodes'],
          pollMs: 30_000,
        }),
      )
    })

    expect(last(history)).toEqual({ phase: 'ready', data: 0 })
    expect(watchCallbacks).toHaveLength(1)
    expect(pollCallbacks).toHaveLength(1)

    await step(() => watchCallbacks[0]())
    await step(() => pollCallbacks[0]())

    expect(fetcher).toHaveBeenCalledTimes(3)
    expect(last(history)).toEqual({ phase: 'ready', data: 2 })

    // The property this hook exists to guarantee: once the first load has
    // landed, neither refresh path may show 'loading' again — the view keeps
    // rendering what it has instead of flashing its skeleton. A future
    // refactor that swapped either path's `run(true)` for `run(false)` would
    // fail this silently without a test pinning it down.
    const firstReady = history.findIndex((s) => s.phase === 'ready')
    expect(history.slice(firstReady + 1).some((s) => s.phase === 'loading')).toBe(false)
  })

  it('a manual reload does show loading — proving the assertion above is not vacuous', async () => {
    let n = 0
    const fetcher = vi.fn(async () => n++)
    const history: LiveState<number>[] = []
    let doReload: () => void = () => {}

    function ReloadHarness() {
      const { state, reload } = useLiveResource<number>(fetcher)
      doReload = reload
      history.push(state)
      return null
    }

    await step(() => root.render(createElement(ReloadHarness)))
    expect(last(history)).toEqual({ phase: 'ready', data: 0 })

    act(() => doReload())
    // Synchronously after a manual reload, the state has already flipped to
    // loading — this is the behaviour a watch/poll-triggered refresh must not
    // have. If it started showing up in the history above, that test would
    // now be exercising something real.
    expect(last(history)).toEqual({ phase: 'loading' })

    await step(() => {})
    expect(last(history)).toEqual({ phase: 'ready', data: 1 })
  })
})
