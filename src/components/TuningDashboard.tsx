import { ReactNode } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Tone, TONE_TEXT } from '@/lib/thresholds'
import { cn } from '@/lib/utils'

/**
 * Shared shell for the eight tuning dashboards (KV-Cache, Elastic Pools,
 * Speculative Decoding, Pipeline PP, MPS, PTP Sync, Burst Buffer, KSM).
 *
 * They were eight copies of the same three cards — summary stats, a per-entity
 * grid, and a config `<pre>` — each re-deriving the same tile markup and colour
 * ladder. This is that structure, once.
 */

export interface StatTile {
  label: string
  value: ReactNode
  tone?: Tone
  /** `sm` for text values ("Ollama / vLLM"), `lg` (default) for numbers. */
  size?: 'sm' | 'lg'
}

export interface TuningDashboardProps<T> {
  /** Summary card heading, e.g. "KSM — Kernel Same-Page Merging". */
  title: string
  icon: ReactNode
  /** Optional right-aligned badge on the summary card (backend, strategy…). */
  badge?: ReactNode
  stats: StatTile[]
  /** Full-width bar under the stat grid, with a caption row beneath it. */
  progress?: {
    value: number
    captionLeft?: ReactNode
    captionRight?: ReactNode
  }
  /**
   * Free slot between the summary and the entity grid — the pipeline diagram,
   * the draft/target model panel, the "how it works" note.
   */
  note?: ReactNode

  entitiesTitle: string
  entities: T[]
  renderEntity: (entity: T, index: number) => ReactNode
  entityKey: (entity: T, index: number) => string
  /** `grid` (default) tiles side by side; `stack` is one full-width row each. */
  entityLayout?: 'grid' | 'stack'
  /** Widest breakpoint column count for the grid layout. */
  entityColumns?: 2 | 3 | 4

  configTitle: string
  config: string
}

const COLUMN_CLASS: Record<2 | 3 | 4, string> = {
  2: 'grid grid-cols-1 sm:grid-cols-2 gap-3',
  3: 'grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3',
  4: 'grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3',
}

/** Stat grid columns follow the tile count instead of being picked per file. */
function statGridClass(count: number): string {
  if (count <= 2) return 'grid grid-cols-2 gap-4'
  if (count === 3) return 'grid grid-cols-2 sm:grid-cols-3 gap-4'
  if (count === 4) return 'grid grid-cols-2 sm:grid-cols-4 gap-4'
  return 'grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-4'
}

export function TuningDashboard<T>({
  title,
  icon,
  badge,
  stats,
  progress,
  note,
  entitiesTitle,
  entities,
  renderEntity,
  entityKey,
  entityLayout = 'grid',
  entityColumns = 3,
  configTitle,
  config,
}: TuningDashboardProps<T>) {
  return (
    <div className="space-y-6">
      {/* ── Summary ─────────────────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            {icon}
            {title}
            {badge && <span className="ml-auto">{badge}</span>}
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className={statGridClass(stats.length)}>
            {stats.map((stat) => (
              <div key={stat.label} className="space-y-1">
                <div className="text-xs text-muted-foreground uppercase tracking-wide">
                  {stat.label}
                </div>
                <div
                  className={cn(
                    'font-mono font-bold',
                    stat.size === 'sm' ? 'text-sm' : 'text-2xl',
                    TONE_TEXT[stat.tone ?? 'foreground'],
                  )}
                >
                  {stat.value}
                </div>
              </div>
            ))}
          </div>

          {progress && (
            <div className="space-y-1">
              <Progress value={progress.value} className="h-3" />
              {(progress.captionLeft || progress.captionRight) && (
                <div className="flex justify-between text-xs text-muted-foreground">
                  <span>{progress.captionLeft}</span>
                  <span>{progress.captionRight}</span>
                </div>
              )}
            </div>
          )}

          {note}
        </CardContent>
      </Card>

      {/* ── Per-entity ──────────────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">{entitiesTitle}</CardTitle>
        </CardHeader>
        <CardContent>
          <div className={entityLayout === 'stack' ? 'space-y-3' : COLUMN_CLASS[entityColumns]}>
            {entities.map((entity, i) => (
              <div key={entityKey(entity, i)}>{renderEntity(entity, i)}</div>
            ))}
          </div>
        </CardContent>
      </Card>

      {/* ── Config reference ────────────────────────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">{configTitle}</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">
            {config}
          </pre>
        </CardContent>
      </Card>
    </div>
  )
}

/**
 * The tile shape every per-entity grid re-implemented: a bordered card with a
 * name + badge header row, then arbitrary body content.
 */
export function EntityTile({
  name,
  badge,
  children,
  className,
}: {
  name: ReactNode
  badge?: ReactNode
  children?: ReactNode
  /** Overrides the default border/background — used to dim disabled entities
   *  and tint faulted ones, which the badge alone under-signals. */
  className?: string
}) {
  return (
    <div
      className={cn(
        'p-3 rounded-lg border border-border bg-secondary/30 space-y-2 h-full',
        className,
      )}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-xs font-bold truncate">{name}</span>
        {badge}
      </div>
      {children}
    </div>
  )
}

/** The `grid-cols-3 text-[10px]` micro-stat triplet under most entity tiles. */
export function MicroStats({
  items,
}: {
  items: { value: ReactNode; label: string; tone?: Tone }[]
}) {
  return (
    <div
      className={cn(
        'grid gap-2 text-[10px] text-muted-foreground',
        items.length === 4 ? 'grid-cols-4' : items.length === 2 ? 'grid-cols-2' : 'grid-cols-3',
      )}
    >
      {items.map((item) => (
        <div key={item.label}>
          <span className={cn('font-mono font-bold', TONE_TEXT[item.tone ?? 'foreground'])}>
            {item.value}
          </span>
          <div>{item.label}</div>
        </div>
      ))}
    </div>
  )
}

/** Labelled bar used inside entity tiles ("Merge ratio 87%" + bar). */
export function TileMeter({
  label,
  value,
  display,
  tone = 'accent',
}: {
  label: string
  /** 0–100. */
  value: number
  /** Defaults to `{value}%`. */
  display?: ReactNode
  tone?: Tone
}) {
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className={cn('font-mono font-bold', TONE_TEXT[tone])}>
          {display ?? `${value.toFixed(0)}%`}
        </span>
      </div>
      <Progress value={Math.max(0, Math.min(100, value))} className="h-1" />
    </div>
  )
}
