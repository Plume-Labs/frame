import { ReactNode } from 'react'
import { Card, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { LiveState } from '@/hooks/useLiveResource'
import { Tone, TONE_TEXT } from '@/lib/thresholds'
import { cn } from '@/lib/utils'
import { ArrowClockwise } from '@phosphor-icons/react'

/**
 * The three blocks every screen draws and each one had redefined: a labelled
 * number, a labelled bar, and the title/refresh row.
 *
 * There were six private `Stat`s and three `Meter`s, differing only in which
 * font size and which subset of the colour ladder they happened to support —
 * so the same reading rendered slightly differently depending on which screen
 * you were looking at. These are those, once.
 */

const SIZE_CLASS = {
  sm: 'text-sm',
  md: 'text-base',
  lg: 'text-2xl',
} as const

export function Stat({
  label,
  value,
  tone = 'foreground',
  size = 'lg',
  icon,
}: {
  label: ReactNode
  /** A node, not just a string — some callers render a badge or a unit here. */
  value: ReactNode
  tone?: Tone
  size?: keyof typeof SIZE_CLASS
  icon?: ReactNode
}) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide flex items-center gap-1">
        {icon}
        {label}
      </div>
      <div className={cn('font-mono font-bold', SIZE_CLASS[size], TONE_TEXT[tone])}>{value}</div>
    </div>
  )
}

/** Column count follows the tile count rather than being chosen per screen. */
export function StatGrid({ children, count }: { children: ReactNode; count: number }) {
  return (
    <div
      className={
        count <= 2
          ? 'grid grid-cols-2 gap-4'
          : count === 3
            ? 'grid grid-cols-2 sm:grid-cols-3 gap-4'
            : count === 4
              ? 'grid grid-cols-2 sm:grid-cols-4 gap-4'
              : 'grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-4'
      }
    >
      {children}
    </div>
  )
}

export function Meter({
  label,
  pct,
  detail,
  tone,
}: {
  label: ReactNode
  pct: number
  /** Right-hand caption. Defaults to the percentage the bar already shows. */
  detail?: ReactNode
  tone?: Tone
}) {
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs font-mono">
        <span className="text-muted-foreground">{label}</span>
        <span className={tone ? TONE_TEXT[tone] : 'text-foreground'}>{detail ?? `${pct.toFixed(0)}%`}</span>
      </div>
      <Progress value={Math.max(0, Math.min(100, pct))} className="h-2" />
    </div>
  )
}

/**
 * Title row with the refresh button. Passing `state` disables the button and
 * spins the icon while a fetch is in flight, which every screen was wiring by
 * hand against its own loading phase.
 */
export function ScreenHeader({
  icon,
  title,
  state,
  onReload,
  actions,
  children,
}: {
  icon?: ReactNode
  title: ReactNode
  state?: LiveState<unknown>
  onReload?: () => void
  /** Extra controls, placed before the refresh button. */
  actions?: ReactNode
  /** Body under the title — a description, badges, a stat grid. */
  children?: ReactNode
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-xl flex items-center gap-2">
          {icon}
          {title}
          <span className="ml-auto flex items-center gap-2">
            {actions}
            {onReload && (
              <Button
                variant="outline"
                size="sm"
                className="font-mono gap-1.5"
                onClick={onReload}
                disabled={state?.phase === 'loading'}
              >
                <ArrowClockwise className={state?.phase === 'loading' ? 'animate-spin' : ''} />
                Refresh
              </Button>
            )}
          </span>
        </CardTitle>
      </CardHeader>
      {children}
    </Card>
  )
}
