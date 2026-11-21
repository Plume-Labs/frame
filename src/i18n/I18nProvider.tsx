import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { I18nContext, type I18n } from './context'
import { detectLocale, LOCALE_STORAGE_KEY, translate, translatePlural, type Locale } from './index'

function readSaved(): string | null {
  try {
    return globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY) ?? null
  } catch {
    return null // storage blocked (private mode, policy): fall back to the browser language
  }
}

/** Holds the current language for the whole console, above the session gate so the login screen is translated too. */
export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() =>
    detectLocale(readSaved(), globalThis.navigator?.languages ?? []),
  )

  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next)
    try {
      globalThis.localStorage?.setItem(LOCALE_STORAGE_KEY, next)
    } catch {
      // The choice still applies for this page load.
    }
  }, [])

  // Screen readers and the browser's own translation prompt follow <html lang>.
  useEffect(() => {
    document.documentElement.lang = locale
  }, [locale])

  const value = useMemo<I18n>(
    () => ({
      locale,
      setLocale,
      t: (key, params) => translate(locale, key, params),
      tn: (base, count, params) => translatePlural(locale, base, count, params),
    }),
    [locale, setLocale],
  )
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}
