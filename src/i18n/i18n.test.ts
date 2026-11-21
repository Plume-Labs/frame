import { describe, expect, it } from 'vitest'
import { catalogues, detectLocale, LOCALES, translate, translatePlural, type MessageKey } from '@/i18n'
import { en } from '@/i18n/en'

const placeholders = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort()

describe('catalogues', () => {
  const keys = Object.keys(en) as MessageKey[]

  for (const { code } of LOCALES) {
    describe(code, () => {
      it('has exactly the source keys, none extra', () => {
        expect(Object.keys(catalogues[code]).sort()).toEqual([...keys].sort())
      })

      it('has no empty string', () => {
        for (const k of keys) expect(catalogues[code][k].trim(), k).not.toBe('')
      })

      it('keeps the same placeholders as English', () => {
        for (const k of keys) expect(placeholders(catalogues[code][k]), k).toEqual(placeholders(en[k]))
      })
    })
  }
})

describe('translate', () => {
  it('fills {name} placeholders and leaves unknown ones visible', () => {
    // Built from a cast: no shipped message takes parameters yet.
    const key = 'common.cancel' as MessageKey
    expect(translate('en', key, { n: 3 })).toBe(en[key])
    const original = catalogues.en[key]
    try {
      catalogues.en[key] = 'Hello {who}, {missing}'
      expect(translate('en', key, { who: 'you' })).toBe('Hello you, {missing}')
    } finally {
      catalogues.en[key] = original
    }
  })

  it('falls back to the key itself for an unknown key', () => {
    expect(translate('fr', 'no.such.key' as MessageKey)).toBe('no.such.key')
  })
})

describe('detectLocale', () => {
  it('prefers a saved choice over the browser', () => {
    expect(detectLocale('en', ['fr-FR'])).toBe('en')
  })

  it('ignores a saved value that is not a shipped locale', () => {
    expect(detectLocale('de', ['fr-FR'])).toBe('fr')
  })

  it('maps a regional tag to its base language and skips unsupported ones', () => {
    expect(detectLocale(null, ['de-DE', 'fr-CA', 'en-US'])).toBe('fr')
  })

  it('falls back to English', () => {
    expect(detectLocale(undefined, ['de', 'ja'])).toBe('en')
    expect(detectLocale(null, [])).toBe('en')
  })
})

describe('rendering', () => {
  it('shows the sign-in screen in French when the locale is fr', async () => {
    const { createElement } = await import('react')
    const { renderToStaticMarkup } = await import('react-dom/server')
    const { I18nContext } = await import('@/i18n/context')
    const { LoginView } = await import('@/components/LoginView')
    const html = renderToStaticMarkup(
      createElement(
        I18nContext.Provider,
        { value: { locale: 'fr', setLocale: () => {}, t: (k, p) => translate('fr', k, p), tn: (b, c, p) => translatePlural('fr', b, c, p) } },
        createElement(LoginView, { onSignedIn: () => {} }),
      ),
    )
    expect(html).toContain(catalogues.fr['login.signIn'].replace(/'/g, '&#x27;'))
    expect(html).not.toContain('Sign in with a passkey')
  })
})

describe('translatePlural', () => {
  it('picks one/other by the locale rule and fills {count}', () => {
    expect(translatePlural('en', 'ksm.note', 1)).toContain('1 node but')
    expect(translatePlural('en', 'ksm.note', 3)).toContain('3 nodes but')
    expect(translatePlural('fr', 'ksm.note', 1)).toContain('1 nœud mais')
    expect(translatePlural('fr', 'ksm.note', 3)).toContain('3 nœuds mais')
  })

  it('has both forms for every counted message in every language', () => {
    for (const code of Object.keys(catalogues) as Array<keyof typeof catalogues>) {
      for (const key of Object.keys(catalogues[code])) {
        if (key.endsWith('.one')) expect(catalogues[code], key).toHaveProperty(key.replace(/\.one$/, '.other'))
      }
    }
  })
})
