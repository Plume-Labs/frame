import { describe, it, expect } from 'vitest'
import { describeLoadError } from './load-error'
import { catalogues, translate, type Locale } from '@/i18n'

const text = (locale: Locale, status: number | undefined) => {
  const c = describeLoadError(status)
  return { title: translate(locale, c.title), hint: translate(locale, c.hint, c.params) }
}

describe('describeLoadError', () => {
  it('tells an expired session from a refused role from a missing resource', () => {
    const titles = [401, 403, 404].map((s) => describeLoadError(s).title)
    expect(new Set(titles).size).toBe(3)
  })

  it('does not call a permission refusal a network problem', () => {
    expect(text('en', 403).title).not.toMatch(/reach/i)
  })

  it('falls back to the proxy hint when there is no status (network failure)', () => {
    expect(text('en', undefined).hint).toMatch(/uiproxy/)
  })

  it('says the API answered when it did, and keeps server trouble apart from a rejected request', () => {
    expect(describeLoadError(418).title).not.toBe(describeLoadError(undefined).title)
    expect(text('en', 418).hint).toContain('418')
    expect(text('fr', 418).hint).toContain('418')
    expect(describeLoadError(501).title).toBe(describeLoadError(503).title)
    expect(describeLoadError(429).title).toBe(describeLoadError(500).title)
    expect(describeLoadError(422).title).not.toBe(describeLoadError(500).title)
  })

  it('has copy for every outcome in every language', () => {
    for (const status of [401, 403, 404, 418, 500, undefined]) {
      const c = describeLoadError(status)
      for (const locale of Object.keys(catalogues) as Locale[]) {
        expect(catalogues[locale][c.title]).toBeTruthy()
        expect(catalogues[locale][c.hint]).toBeTruthy()
      }
    }
  })
})
