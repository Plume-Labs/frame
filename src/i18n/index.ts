import { en } from './en'
import { fr } from './fr'

/**
 * Console translation, with no runtime dependency.
 *
 * `en.ts` is the source: its keys are the `MessageKey` type, and every other
 * catalogue is a `Record<MessageKey, string>`, so a missing translation is a
 * compile error rather than a blank label. To add a language: create
 * `<code>.ts` next to `fr.ts`, add it to `CATALOGUES` and `LOCALES` below.
 */
export type MessageKey = keyof typeof en
export type Params = Record<string, string | number>

export const LOCALES = [
  // Each language is named in itself, so it is findable whatever the current one.
  { code: 'en', label: 'English' },
  { code: 'fr', label: 'Français' },
] as const

export type Locale = (typeof LOCALES)[number]['code']

const CATALOGUES: Record<Locale, Record<MessageKey, string>> = { en, fr }

export const DEFAULT_LOCALE: Locale = 'en'
export const LOCALE_STORAGE_KEY = 'hearthrack.locale'

export function isLocale(value: unknown): value is Locale {
  return LOCALES.some((l) => l.code === value)
}

/**
 * The language to open in: the user's saved choice, else the first browser
 * language we ship (`fr-CA` → `fr`), else English.
 */
export function detectLocale(saved: string | null | undefined, browserLanguages: readonly string[]): Locale {
  if (isLocale(saved)) return saved
  for (const tag of browserLanguages) {
    const base = tag.toLowerCase().split('-')[0]
    if (isLocale(base)) return base
  }
  return DEFAULT_LOCALE
}

/**
 * Looks a key up and fills its `{name}` placeholders. A key missing from the
 * catalogue (only reachable through a cast) falls back to English, then to the
 * key itself, so a gap shows up as readable text instead of a crash.
 */
export function translate(locale: Locale, key: MessageKey, params?: Params): string {
  const template = CATALOGUES[locale][key] ?? en[key] ?? key
  if (!params) return template
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => (name in params ? String(params[name]) : whole))
}

/** Every catalogue, for the tests that compare them. */
export const catalogues: Record<Locale, Record<MessageKey, string>> = CATALOGUES
