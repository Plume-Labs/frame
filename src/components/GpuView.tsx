import { GpuInfo, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Stat, Meter } from '@/components/Primitives'
import { TrendRow } from '@/components/Sparkline'
import { inverseScoreTone, TONE_TEXT } from '@/lib/thresholds'
import { Speedometer, ArrowClockwise, Thermometer, Lightning, TrendUp } from '@phosphor-icons/react'

const frame = createFrameClient()

const WINDOW_HOURS = 3

/**
 * The three signals that explain each other: utilisation is the work, power is
 * what that work draws, and temperature is what the card does with the heat.
 * Utilisation flat while temperature climbs is a cooling problem; power capped
 * below the limit while utilisation sits at 100% is a throttle.
 */
const TREND_QUERIES = [
  { metric: 'Utilisation', q: 'avg(DCGM_FI_DEV_GPU_UTIL)', unit: '%' },
  { metric: 'Temperature', q: 'max(DCGM_FI_DEV_GPU_TEMP)', unit: '°C' },
  { metric: 'Power draw', q: 'sum(DCGM_FI_DEV_POWER_USAGE)', unit: ' W' },
]

// Tesla-class cards throttle in the mid-80s, so 75 is the comfortable ceiling
// and 85 is where sustained throughput starts being clipped.
const TEMP_GOOD_C = 75
const TEMP_OK_C = 85

export function GpuView() {
  const { state, reload } = useLiveResource<GpuInfo[]>(() => frame.cluster.gpus())
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(() =>
    frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
  )
  const gpus = state.phase === 'ready' ? state.data : []
  const trend = trendState.phase === 'ready' ? trendState.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Speedometer className="text-primary" />
            GPU
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && (
          <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <Stat label="GPUs" value={`${gpus.length}`} tone="accent" />
            <Stat label="Avg util" value={`${gpus.length ? Math.round(gpus.reduce((s, g) => s + g.utilPct, 0) / gpus.length) : 0}%`} />
            <Stat label="Total power" value={`${gpus.reduce((s, g) => s + g.powerW, 0).toFixed(0)} W`} />
            <Stat label="Model" value={gpus[0]?.model ?? '—'} size="sm" />
          </CardContent>
        )}
      </Card>

      {trend && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-base flex items-center gap-2">
              <TrendUp size={16} className="text-primary" /> Last {WINDOW_HOURS}h (Prometheus / DCGM)
            </CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-1 sm:grid-cols-3 gap-6">
            {trend.map((series) => (
              <TrendRow
                key={series.metric}
                series={series}
                windowHours={WINDOW_HOURS}
                tone={series.metric === 'Temperature' ? inverseScoreTone(series.current, TEMP_GOOD_C, TEMP_OK_C) : undefined}
              />
            ))}
          </CardContent>
        </Card>
      )}

      <LiveStates state={state} emptyLabel="No GPUs / DCGM exporter not present." />

      {state.phase === 'ready' &&
        gpus.map((g) => {
          const memPct = g.memTotalMB ? (g.memUsedMB / g.memTotalMB) * 100 : 0
          return (
            <Card key={`${g.node}-${g.index}`}>
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div>
                    <CardTitle className="font-mono text-lg">{g.model} #{g.index}</CardTitle>
                    <p className="text-xs text-muted-foreground font-mono">{g.node}</p>
                  </div>
                  <div className="flex gap-2">
                    <Badge
                      variant="outline"
                      className={`font-mono text-[10px] gap-1 ${TONE_TEXT[inverseScoreTone(g.tempC, TEMP_GOOD_C, TEMP_OK_C)]}`}
                    >
                      <Thermometer size={11} />
                      {g.tempC}°C
                    </Badge>
                    <Badge variant="outline" className="font-mono text-[10px] gap-1"><Lightning size={11} />{g.powerW.toFixed(0)}W</Badge>
                  </div>
                </div>
              </CardHeader>
              <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <Meter label="GPU utilisation" pct={g.utilPct} detail={`${g.utilPct}%`} />
                <Meter label="Memory" pct={memPct} detail={`${(g.memUsedMB / 1024).toFixed(1)} / ${(g.memTotalMB / 1024).toFixed(1)} GiB`} />
                <div className="grid grid-cols-2 gap-2 text-[10px] text-muted-foreground font-mono sm:col-span-2">
                  <div><span className="text-foreground font-bold">{g.encUtil}%</span> encoder</div>
                  <div><span className="text-foreground font-bold">{g.decUtil}%</span> decoder</div>
                </div>
              </CardContent>
            </Card>
          )
        })}
    </div>
  )
}


