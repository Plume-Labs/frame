import { CapacityResource, ForecastStatus, coreListPath, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { TrendRow } from '@/components/Sparkline'
import { inverseScoreClass, inverseScoreTone } from '@/lib/thresholds'
import { ChartLine, ArrowClockwise, TrendUp } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'

const frame = createFrameClient()

// Resource and forecast metric names come from the SDK; map the known ones.
const RESOURCE_LABEL: Record<string, MessageKey> = {
  CPU: 'cap.res.cpu',
  Memory: 'cap.res.memory',
}

const pct = (a: number, b: number) => (b > 0 ? (a / b) * 100 : 0)
// Usage percentages: lower is better, comfortable below 75, tight past 90.
const capacityTone = (p: number) => inverseScoreTone(p, 75, 90)
const tone = (p: number) => inverseScoreClass(p, 75, 90)

export function CapacityView() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<CapacityResource[]>(
    () => frame.cluster.capacity(),
    [],
    // Allocatable/requested come straight from Node and Pod objects, watched.
    [coreListPath('nodes'), coreListPath('pods')],
    // "Used (live)" layers metrics-server on top, best-effort, with no watch
    // support and continuous drift the object watch above cannot see. Slow
    // poll: the watch already covers every discrete change, this only tops
    // up the one number that moves on its own.
    30_000,
  )
  const res = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ChartLine className="text-primary" />
            {t('cap.title')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            {t('cap.desc')}
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel={t('cap.empty')} />

      {state.phase === 'ready' &&
        res.map((r) => {
          const usedPct = pct(r.used, r.allocatable)
          const reqPct = pct(r.requested, r.allocatable)
          return (
            <Card key={r.name}>
              <CardHeader>
                <div className="flex items-center justify-between">
                  <CardTitle className="font-mono text-lg">{RESOURCE_LABEL[r.name] ? t(RESOURCE_LABEL[r.name]) : r.name}</CardTitle>
                  <Badge variant="outline" className={`font-mono ${tone(reqPct)} border-current`}>
                    {t('cap.reserved', { pct: reqPct.toFixed(0) })}
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="space-y-4">
                <Bar
                  label={t('cap.used')}
                  value={usedPct}
                  detail={`${r.used.toFixed(1)} / ${r.allocatable.toFixed(1)} ${r.unit}`}
                />
                <Bar
                  label={t('cap.requested')}
                  value={reqPct}
                  detail={`${r.requested.toFixed(1)} / ${r.allocatable.toFixed(1)} ${r.unit}`}
                />
                <div className="text-xs text-muted-foreground font-mono">
                  {t('cap.headroom', {
                    unreserved: `${(r.allocatable - r.requested).toFixed(1)} ${r.unit}`,
                    unused: `${(r.allocatable - r.used).toFixed(1)} ${r.unit}`,
                  })}
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
  const { t } = useI18n()
  // A Prometheus projection over a 3h history — a "days to full" number that
  // does not need to be fresher than a minute, unlike the gauges above it.
  const { state } = useLiveResource<ForecastStatus | null>(() => frame.cluster.forecast(), [], [], 60_000)
  const fc = state.phase === 'ready' ? state.data : null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <TrendUp size={16} className="text-primary" /> {t('cap.forecast.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <LiveStates state={state} emptyLabel={t('cap.forecast.empty')} />
        {fc &&
          fc.series.map((s) => (
            <TrendRow
              key={s.metric}
              series={s}
              label={RESOURCE_LABEL[s.metric] ? t(RESOURCE_LABEL[s.metric]) : undefined}
              windowHours={fc.windowHours}
              tone={capacityTone(s.current)}
              caption={
                s.projectedFullDays == null
                  ? t('cap.caption.flat', { hours: fc.windowHours })
                  : t('cap.caption.days', { hours: fc.windowHours, days: s.projectedFullDays.toFixed(1) })
              }
            />
          ))}
      </CardContent>
    </Card>
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
