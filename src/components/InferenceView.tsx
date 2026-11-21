import { InferenceStatus, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { DECODE_RATE_QUERY, formatRate } from '@/lib/inference-rates'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { TuningDashboard } from '@/components/TuningDashboard'
import { InferenceServicesPanel } from '@/components/InferenceServicesPanel'
import { TrendRow } from '@/components/Sparkline'
import { inverseScoreTone } from '@/lib/thresholds'
import { Lightning, Database } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'

const frame = createFrameClient()

const WINDOW_HOURS = 3

/**
 * KV depth and decode rate belong on the same screen because they trade against
 * each other: as the cache fills, the attention window each token is decoded
 * against grows, and throughput falls. Seeing one without the other turns a
 * predictable slowdown into a mystery.
 */
const TREND_QUERIES = [
  { metric: 'KV depth', q: 'llamacpp:n_tokens_max', unit: ' tok' },
  { metric: 'Decode rate', q: DECODE_RATE_QUERY, unit: ' tok/s' },
  { metric: 'Requests processing', q: 'llamacpp:requests_processing' },
]

const TREND_LABEL: Record<string, MessageKey> = {
  'KV depth': 'inf.trend.kv',
  'Decode rate': 'inf.trend.decode',
  'Requests processing': 'inf.trend.processing',
}

export function InferenceView() {
  const { t } = useI18n()
  // llama.cpp's own /metrics, not a Kubernetes object — nothing to watch.
  // Decode rate and KV depth move within a single generation, so this wants a
  // faster read than the slower node-level gauges elsewhere.
  const { state, reload } = useLiveResource<InferenceStatus | null>(() => frame.cluster.inference(), [], [], 10_000)
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(
    () => frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
    [],
    [],
    30_000,
  )
  const inf = state.phase === 'ready' ? state.data : null
  const trend = trendState.phase === 'ready' ? trendState.data : null

  return (
    <div className="space-y-6">
    {/* Outside the dashboard, which renders only an empty state when the chat
        server is down — exactly when its Start button is needed. */}
    <InferenceServicesPanel inFlight={inf?.requestsProcessing} />
    <TuningDashboard
      title={t('inf.title')}
      icon={<Lightning className="text-primary" />}
      state={state}
      onReload={reload}
      emptyLabel={t('inf.empty')}
      stats={
        inf
          ? [
              { label: t('inf.decode'), value: formatRate(inf.predictedTokensPerSec), tone: 'accent' },
              { label: t('inf.prompt'), value: formatRate(inf.promptTokensPerSec) },
              { label: t('inf.processing'), value: `${inf.requestsProcessing}` },
              {
                label: t('inf.deferred'),
                value: `${inf.requestsDeferred}`,
                // Anything deferred means requests are queueing behind the slots.
                tone: inverseScoreTone(inf.requestsDeferred, 0, 0),
              },
              { label: t('inf.busySlots'), value: inf.busySlotsPerDecode.toFixed(2), size: 'sm' },
            ]
          : []
      }
      progress={
        inf
          ? {
              value: Math.max(0, Math.min(100, inf.kvUsePct)),
              captionLeft: t('inf.kvCaption', { used: inf.kvTokens, ctx: inf.nCtx }),
              captionRight: t('inf.kvPct', { pct: inf.kvUsePct.toFixed(1) }),
            }
          : undefined
      }
      trend={
        trend && (
          <>
            {trend.map((series) => (
              <TrendRow
                key={series.metric}
                series={series}
                label={TREND_LABEL[series.metric] ? t(TREND_LABEL[series.metric]) : undefined}
                windowHours={WINDOW_HOURS}
                format={(v) => v.toFixed(1)}
              />
            ))}
          </>
        )
      }
      note={
        inf && (
          <div className="flex flex-wrap gap-2 text-xs">
            <Badge variant="outline" className="font-mono gap-1">
              <Database size={12} />
              {inf.model}
            </Badge>
            <Badge variant="outline" className="font-mono">
              {inf.node}
            </Badge>
            <Badge variant="outline" className="font-mono">
              {t('inf.slots', { n: inf.slots })}
            </Badge>
            <Badge variant="outline" className="font-mono">
              {t('inf.ctx', { n: inf.nCtx })}
            </Badge>
          </div>
        )
      }
      entitiesTitle={t('inf.cumulative')}
      entities={inf ? [inf] : []}
      entityKey={() => 'totals'}
      entityColumns={2}
      renderEntity={(i) => (
        <div className="grid grid-cols-2 gap-4 text-xs font-mono">
          <div>
            <div className="text-muted-foreground">{t('inf.promptTokens')}</div>
            <div className="text-foreground font-bold">{i.promptTokensTotal.toLocaleString()}</div>
          </div>
          <div>
            <div className="text-muted-foreground">{t('inf.decodedTokens')}</div>
            <div className="text-foreground font-bold">{i.tokensPredictedTotal.toLocaleString()}</div>
          </div>
        </div>
      )}
    />
    </div>
  )
}
