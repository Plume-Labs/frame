import { MetricSeries } from '@/lib/frame-sdk'
import { Tone, TONE_TEXT } from '@/lib/thresholds'
import { cn } from '@/lib/utils'

/**
 * Inline SVG sparkline — no chart dependency. Scales the series to its own
 * viewBox, so the shape is readable whatever the units; the caller supplies the
 * numbers that give it scale.
 *
 * Lived inside CapacityView until every screen wanted one.
 */
export function Sparkline({ points, className }: { points: number[]; className?: string }) {
  if (points.length < 2) {
    return <div className="h-8 text-[10px] text-muted-foreground font-mono">not enough history yet</div>
  }
  const min = Math.min(...points)
  const max = Math.max(...points)
  const span = max - min || 1
  const w = 300
  const h = 32
  const d = points
    .map((v, i) => {
      const x = (i / (points.length - 1)) * w
      const y = h - ((v - min) / span) * (h - 2) - 1
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className={cn('w-full h-8', className ?? 'text-primary')} preserveAspectRatio="none">
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  )
}

/**
 * One metric's history as a labelled row: name, current value, the curve, and a
 * caption. The screens that show trends all want this same block, so it lives
 * here rather than being retyped per view.
 *
 * A flat sparkline is still information — it says the metric is steady — so an
 * empty history renders the "not enough history yet" note instead of vanishing.
 */
export function TrendRow({
  series,
  windowHours,
  tone,
  format = (v) => v.toFixed(0),
  caption,
}: {
  series: MetricSeries
  windowHours: number
  /** Colour for the current value; the curve stays primary so rows read alike. */
  tone?: Tone
  format?: (v: number) => string
  /** Replaces the default "last Nh" note — use it for a projection or a rate. */
  caption?: string
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between text-xs font-mono">
        <span className="text-muted-foreground">{series.metric}</span>
        <span className={tone ? TONE_TEXT[tone] : 'text-foreground'}>
          {format(series.current)}
          {series.unit ?? ''}
        </span>
      </div>
      <Sparkline points={series.history.map((p) => p.v)} />
      <div className="text-[10px] text-muted-foreground font-mono">{caption ?? `last ${windowHours}h`}</div>
    </div>
  )
}
