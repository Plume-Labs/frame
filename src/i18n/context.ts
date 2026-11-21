import { createContext, useContext } from 'react'
import { DEFAULT_LOCALE, translate, translatePlural, type Locale, type MessageKey, type Params, type PluralKey } from './index'

export interface I18n {
  locale: Locale
  setLocale: (locale: Locale) => void
  t: (key: MessageKey, params?: Params) => string
  /** Counted text: `tn('nodes', 3)` picks `nodes.one` / `nodes.other`; `{count}` is filled in. */
  tn: (base: PluralKey, count: number, params?: Params) => string
}

// The default value is working English, not `null`: a component rendered
// without a provider (a unit test, a story) still shows text instead of
// throwing.
export const I18nContext = createContext<I18n>({
  locale: DEFAULT_LOCALE,
  setLocale: () => {},
  t: (key, params) => translate(DEFAULT_LOCALE, key, params),
  tn: (base, count, params) => translatePlural(DEFAULT_LOCALE, base, count, params),
})

export function useI18n(): I18n {
  return useContext(I18nContext)
}
