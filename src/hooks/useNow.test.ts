// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot } from 'react-dom/client'

import { useNow } from './useNow'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => vi.useRealTimers())

describe('useNow', () => {
  it('starts at the current time and moves on its own tick, not on every render', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-16T09:00:00Z'))
    const seen: number[] = []
    function Probe({ n }: { n: number }) {
      seen.push(useNow(1000))
      return n ? null : null
    }
    const root = createRoot(document.createElement('div'))
    act(() => root.render(createElement(Probe, { n: 1 })))
    const first = seen[seen.length - 1]
    expect(first).toBe(Date.parse('2026-11-16T09:00:00Z'))

    // Time passes and the component renders again for another reason: same value.
    vi.setSystemTime(new Date('2026-11-16T09:00:00.500Z'))
    act(() => root.render(createElement(Probe, { n: 2 })))
    expect(seen[seen.length - 1]).toBe(first)

    act(() => vi.advanceTimersByTime(1000))
    expect(seen[seen.length - 1]).toBe(Date.parse('2026-11-16T09:00:01.500Z'))

    act(() => root.unmount())
    expect(vi.getTimerCount()).toBe(0)
  })
})
