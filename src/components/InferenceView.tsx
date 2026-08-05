import { InferenceStatus, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { TuningDashboard } from '@/components/TuningDashboard'
import { TrendRow } from '@/components/Sparkline'
import { inverseScoreTone } from '@/lib/thresholds'
import { Lightning, Database } from '@phosphor-icons/react'

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
  { metric: 'Decode rate', q: 'llamacpp:predicted_tokens_seconds', unit: ' tok/s' },
  { metric: 'Requests processing', q: 'llamacpp:requests_processing' },
]

export function InferenceView() {
  const { state, reload } = useLiveResource<InferenceStatus | null>(() => frame.cluster.inference())
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(() =>
    frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
  )
  const inf = state.phase === 'ready' ? state.data : null
  const trend = trendState.phase === 'ready' ? trendState.data : null

  return (
    <TuningDashboard
      title="KV-Cache & Inference"
      icon={<Lightning className="text-primary" />}
      state={state}
      onReload={reload}
      emptyLabel="No inference server (llama.cpp) deployed."
      stats={
        inf
          ? [
              { label: 'Decode tok/s', value: inf.predictedTokensPerSec.toFixed(1), tone: 'accent' },
              { label: 'Prompt tok/s', value: inf.promptTokensPerSec.toFixed(1) },
              { label: 'Processing', value: `${inf.requestsProcessing}` },
              {
                label: 'Deferred',
                value: `${inf.requestsDeferred}`,
                // Anything deferred means requests are queueing behind the slots.
                tone: inverseScoreTone(inf.requestsDeferred, 0, 0),
              },
              { label: 'Busy slots/decode', value: inf.busySlotsPerDecode.toFixed(2), size: 'sm' },
            ]
          : []
      }
      progress={
        inf
          ? {
              value: Math.max(0, Math.min(100, inf.kvUsePct)),
              captionLeft: `KV ${inf.kvTokens} / ${inf.nCtx} tokens`,
              captionRight: `${inf.kvUsePct.toFixed(1)}% of context window`,
            }
          : undefined
      }
      trend={
        trend && (
          <>
            {trend.map((series) => (
              <TrendRow key={series.metric} series={series} windowHours={WINDOW_HOURS} format={(v) => v.toFixed(1)} />
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
              {inf.slots} slots
            </Badge>
            <Badge variant="outline" className="font-mono">
              ctx {inf.nCtx}
            </Badge>
          </div>
        )
      }
      entitiesTitle="Cumulative"
      entities={inf ? [inf] : []}
      entityKey={() => 'totals'}
      entityColumns={2}
      renderEntity={(i) => (
        <div className="grid grid-cols-2 gap-4 text-xs font-mono">
          <div>
            <div className="text-muted-foreground">Prompt tokens</div>
            <div className="text-foreground font-bold">{i.promptTokensTotal.toLocaleString()}</div>
          </div>
          <div>
            <div className="text-muted-foreground">Decoded tokens</div>
            <div className="text-foreground font-bold">{i.tokensPredictedTotal.toLocaleString()}</div>
          </div>
        </div>
      )}
    />
  )
}
