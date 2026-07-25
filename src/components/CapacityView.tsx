import { CapacityResource, ForecastStatus, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ChartLine, ArrowClockwise, TrendUp } from '@phosphor-icons/react'

const frame = createFrameClient()

const pct = (a: number, b: number) => (b > 0 ? (a / b) * 100 : 0)
const tone = (p: number) => (p >= 90 ? 'text-destructive' : p >= 75 ? 'text-warning' : 'text-accent')

export function CapacityView() {
  const { state, reload } = useLiveResource<CapacityResource[]>(() => frame.cluster.capacity())
  const res = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ChartLine className="text-primary" />
            Cluster Capacity
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Live allocatable capacity vs actual usage (metrics-server) and reserved requests, plus a
            usage trend and projection from Prometheus history (below).
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No capacity data." />

      {state.phase === 'ready' &&
        res.map((r) => {
          const usedPct = pct(r.used, r.allocatable)
          const reqPct = pct(r.requested, r.allocatable)
          return (
            <Card key={r.name}>
              <CardHeader>
                <div className="flex items-center justify-between">
                  <CardTitle className="font-mono text-lg">{r.name}</CardTitle>
                  <Badge variant="outline" className={`font-mono ${tone(reqPct)} border-current`}>
                    {reqPct.toFixed(0)}% reserved
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="space-y-4">
                <Bar
                  label="Used (live)"
                  value={usedPct}
                  detail={`${r.used.toFixed(1)} / ${r.allocatable.toFixed(1)} ${r.unit}`}
                />
                <Bar
                  label="Requested (reserved)"
                  value={reqPct}
                  detail={`${r.requested.toFixed(1)} / ${r.allocatable.toFixed(1)} ${r.unit}`}
                />
                <div className="text-xs text-muted-foreground font-mono">
                  Headroom: {(r.allocatable - r.requested).toFixed(1)} {r.unit} unreserved ·{' '}
                  {(r.allocatable - r.used).toFixed(1)} {r.unit} unused
                </div>
              </CardContent>
            </Card>
          )
        })}

      <ForecastCard />
    </div>
  )
}

function ForecastCard() {
  const { state } = useLiveResource<ForecastStatus | null>(() => frame.cluster.forecast())
  const fc = state.phase === 'ready' ? state.data : null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <TrendUp size={16} className="text-primary" /> Usage trend &amp; forecast (Prometheus)
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <LiveStates state={state} emptyLabel="Prometheus not deployed." />
        {fc &&
          fc.series.map((s) => (
            <div key={s.metric} className="space-y-1">
              <div className="flex items-center justify-between text-xs font-mono">
                <span className="text-muted-foreground">{s.metric}</span>
                <span className={tone(s.current)}>{s.current.toFixed(0)}%</span>
              </div>
              <Sparkline points={s.history.map((p) => p.v)} />
              <div className="text-[10px] text-muted-foreground font-mono">
                last {fc.windowHours}h ·{' '}
                {s.projectedFullDays == null
                  ? 'flat/declining — no exhaustion projected'
                  : `~${s.projectedFullDays.toFixed(1)} days to 100% at current trend`}
              </div>
            </div>
          ))}
      </CardContent>
    </Card>
  )
}

// Inline SVG sparkline — no chart dep. Scales the series to a 0-100 viewBox.
function Sparkline({ points }: { points: number[] }) {
  if (points.length < 2) return <div className="h-8 text-[10px] text-muted-foreground font-mono">not enough history yet</div>
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
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full h-8" preserveAspectRatio="none">
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" className="text-primary" />
    </svg>
  )
}

function Bar({ label, value, detail }: { label: string; value: number; detail: string }) {
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs font-mono">
        <span className="text-muted-foreground">{label}</span>
        <span className={tone(value)}>{value.toFixed(0)}%</span>
      </div>
      <Progress value={Math.min(100, value)} className="h-2" />
      <div className="text-[10px] text-muted-foreground font-mono">{detail}</div>
    </div>
  )
}
