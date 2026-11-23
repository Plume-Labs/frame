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
/** A parameter may itself be a `Message`, translated in the same language: `{ dur: { key: 'x.min', params: { n: 6 } } }`. */
export type Param = string | number | Message
export type Params = Record<string, Param>

/** Text a pure module cannot render itself: a message key plus its parameters. The UI renders it with `useI18n().tm`. */
export interface Message {
  key: MessageKey
  params?: Params
}

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
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => {
    if (!(name in params)) return whole
    const v = params[name]
    return typeof v === 'object' ? translate(locale, v.key, v.params) : String(v)
  })
}

/** The base of a `<base>.one` / `<base>.other` pair of messages, for counted text. */
export type PluralKey = MessageKey extends infer K ? (K extends `${infer B}.one` ? B : never) : never

/**
 * Counted text: picks `<base>.one` or `<base>.other` by the locale's plural
 * rule (`Intl.PluralRules`) and fills `{count}` plus any other params. Only
 * `one` and `other` are used — enough for English and French; a language that
 * needs more categories adds `<base>.few` etc. and they are picked up here.
 */
export function translatePlural(locale: Locale, base: PluralKey, count: number, params?: Params): string {
  const category = new Intl.PluralRules(locale).select(count)
  const key = `${base}.${category}` as MessageKey
  const chosen = key in CATALOGUES[locale] ? key : (`${base}.other` as MessageKey)
  return translate(locale, chosen, { count, ...params })
}

/** Every catalogue, for the tests that compare them. */
export const catalogues: Record<Locale, Record<MessageKey, string>> = CATALOGUES
