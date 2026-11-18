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
  })

  it('says the API answered when it did, and keeps server trouble apart from a rejected request', () => {
    expect(describeLoadError(418).title).not.toBe(describeLoadError(undefined).title)
    expect(describeLoadError(418).hint).toContain('418')
    expect(describeLoadError(501).title).toBe(describeLoadError(503).title)
    expect(describeLoadError(429).title).toBe(describeLoadError(500).title)
    expect(describeLoadError(422).title).not.toBe(describeLoadError(500).title)
  })
})
