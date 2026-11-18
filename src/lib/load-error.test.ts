import { describe, it, expect } from 'vitest'
import { describeLoadError } from './load-error'

describe('describeLoadError', () => {
  it('tells an expired session from a refused role from a missing resource', () => {
    const titles = [401, 403, 404].map((s) => describeLoadError(s).title)
    expect(new Set(titles).size).toBe(3)
  })

  it('does not call a permission refusal a network problem', () => {
    expect(describeLoadError(403).title).not.toMatch(/reach/i)
  })

  it('falls back to the proxy hint when there is no status (network failure)', () => {
    expect(describeLoadError(undefined).hint).toMatch(/uiproxy/)
    expect(describeLoadError(418).title).toBe(describeLoadError(undefined).title)
  })
})
