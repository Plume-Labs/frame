import { createContext, useContext } from 'react'
import { DEFAULT_LOCALE, translate, type Locale, type MessageKey, type Params } from './index'

export interface I18n {
  locale: Locale
  setLocale: (locale: Locale) => void
  t: (key: MessageKey, params?: Params) => string
}

// The default value is working English, not `null`: a component rendered
// without a provider (a unit test, a story) still shows text instead of
// throwing.
export const I18nContext = createContext<I18n>({
  locale: DEFAULT_LOCALE,
  setLocale: () => {},
  t: (key, params) => translate(DEFAULT_LOCALE, key, params),
})

export function useI18n(): I18n {
  return useContext(I18nContext)
}
