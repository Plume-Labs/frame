import { useI18n } from '@/i18n/context'
import { LOCALES } from '@/i18n'
import { cn } from '@/lib/utils'

/** The language choice as a segmented control; each language is named in itself. */
export function LanguageSwitcher({ className }: { className?: string }) {
  const { locale, setLocale, t } = useI18n()
  return (
    <div role="group" aria-label={t('common.language')} className={cn('inline-flex rounded border border-border text-[10px] font-mono', className)}>
      {LOCALES.map((l) => (
        <button
          key={l.code}
          type="button"
          lang={l.code}
          aria-pressed={locale === l.code}
          onClick={() => setLocale(l.code)}
          className={cn(
            'px-2 py-1 first:rounded-l last:rounded-r',
            locale === l.code ? 'bg-primary text-primary-foreground' : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {l.label}
        </button>
      ))}
    </div>
  )
}
