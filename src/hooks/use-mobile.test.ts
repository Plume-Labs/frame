// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot } from 'react-dom/client'

import { useIsMobile } from './use-mobile'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

afterEach(() => vi.unstubAllGlobals())

function stubViewport(width: number) {
  const listeners = new Set<() => void>()
  vi.stubGlobal('innerWidth', width)
  vi.stubGlobal(
    'matchMedia',
    vi.fn(() => ({
      addEventListener: (_: string, l: () => void) => listeners.add(l),
      removeEventListener: (_: string, l: () => void) => listeners.delete(l),
    })),
  )
  return {
    resize(to: number) {
      vi.stubGlobal('innerWidth', to)
      listeners.forEach((l) => l())
    },
    listeners,
  }
}

function mount() {
  const seen: boolean[] = []
  function Probe() {
    seen.push(useIsMobile())
    return null
  }
  const root = createRoot(document.createElement('div'))
  act(() => root.render(createElement(Probe)))
  return { seen, root }
}

describe('useIsMobile', () => {
  it('reads the width on the first render and follows the breakpoint', () => {
    const vp = stubViewport(500)
    const { seen, root } = mount()
    expect(seen[0]).toBe(true)

    act(() => vp.resize(1200))
    expect(seen[seen.length - 1]).toBe(false)
    act(() => vp.resize(767))
    expect(seen[seen.length - 1]).toBe(true)

    act(() => root.unmount())
    expect(vp.listeners.size).toBe(0)
  })
})
