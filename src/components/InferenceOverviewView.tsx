import { GpuInfo, InferenceStatus, MetricSeries, TeiStatus, createFrameClient } from '@/lib/frame-sdk'
import { DECODE_RATE_QUERY, formatRate } from '@/lib/inference-rates'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Stat, Meter } from '@/components/Primitives'
import { TrendRow } from '@/components/Sparkline'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'
import { Lightning, ArrowClockwise, Database, Gauge, Speedometer, Thermometer, TrendUp } from '@phosphor-icons/react'

const frame = createFrameClient()

const WINDOW_HOURS = 3

/**
 * The serving chain end to end: what the GPU is doing, what the model gets out
 * of it, and how deep the queue behind it is. Read together they say whether a
 * slow response is the hardware, the model, or a backlog.
 */
const TREND_QUERIES = [
  { metric: 'GPU utilisation', q: 'avg(DCGM_FI_DEV_GPU_UTIL)', unit: '%' },
  { metric: 'Decode rate', q: DECODE_RATE_QUERY, unit: ' tok/s' },
  { metric: 'Requests processing', q: 'llamacpp:requests_processing' },
]

const TREND_LABEL: Record<string, MessageKey> = {
  'GPU utilisation': 'ovi.trend.gpu',
  'Decode rate': 'inf.trend.decode',
  'Requests processing': 'inf.trend.processing',
}

/**
 * Single-glance serving overview: GPU (DCGM) + llama.cpp (GPU-backed chat
 * completions) + TEI (CPU-only embeddings) side by side. The detailed
 * per-surface breakdowns still live on their own screens (GPU, KV-Cache).
 */
export function InferenceOverviewView() {
  const { t } = useI18n()
  // All three are proxied Prometheus /metrics endpoints, not Kubernetes
  // objects — nothing to watch. GPU and llama.cpp move within a single
  // generation; TEI's embedding queue a little slower.
  const gpuState = useLiveResource<GpuInfo[]>(() => frame.cluster.gpus(), [], [], 10_000)
  const llamaState = useLiveResource<InferenceStatus | null>(() => frame.cluster.inference(), [], [], 10_000)
  const teiState = useLiveResource<TeiStatus | null>(() => frame.cluster.teiStatus(), [], [], 15_000)
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(
    () => frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
    [],
    [],
    30_000,
  )
  const trend = trendState.phase === 'ready' ? trendState.data : null

  const loading = gpuState.state.phase === 'loading' || llamaState.state.phase === 'loading' || teiState.state.phase === 'loading'

  function reloadAll() {
    gpuState.reload()
    llamaState.reload()
    teiState.reload()
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Lightning className="text-primary" />
            {t('ovi.title')}
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reloadAll} disabled={loading}>
              <ArrowClockwise className={loading ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-muted-foreground font-mono">
            {t('ovi.desc')}
          </p>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <GpuCard state={gpuState.state} />
        <LlamaCppCard state={llamaState.state} />
        <TeiCard state={teiState.state} />
      </div>

      {trend && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-base flex items-center gap-2">
              <TrendUp size={16} className="text-primary" /> {t('ovi.trendTitle', { hours: WINDOW_HOURS })}
            </CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-1 sm:grid-cols-3 gap-6">
            {trend.map((series) => (
              <TrendRow key={series.metric} series={series} label={TREND_LABEL[series.metric] ? t(TREND_LABEL[series.metric]) : undefined} windowHours={WINDOW_HOURS} format={(v) => v.toFixed(1)} />
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function GpuCard({ state }: { state: ReturnType<typeof useLiveResource<GpuInfo[]>>['state'] }) {
  const { t } = useI18n()
  const gpu = state.phase === 'ready' ? state.data[0] : undefined
  const memPct = gpu?.memTotalMB ? (gpu.memUsedMB / gpu.memTotalMB) * 100 : 0

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Speedometer size={16} className="text-primary" />
          {t('gpu.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <LiveStates state={state} emptyLabel={t('ovi.gpu.empty')} />
        {gpu && (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline" className="font-mono text-[10px]">{gpu.model} #{gpu.index}</Badge>
              <Badge variant="outline" className="font-mono text-[10px] gap-1"><Thermometer size={11} />{gpu.tempC}°C</Badge>
              <Badge variant="outline" className="font-mono text-[10px] gap-1"><Lightning size={11} />{gpu.powerW.toFixed(0)}W</Badge>
            </div>
            <Meter label={t('ovi.utilisation')} pct={gpu.utilPct} detail={`${gpu.utilPct}%`} />
            <Meter label={t('gpu.meter.memory')} pct={memPct} detail={`${(gpu.memUsedMB / 1024).toFixed(1)} / ${(gpu.memTotalMB / 1024).toFixed(1)} GiB`} />
          </>
        )}
      </CardContent>
    </Card>
  )
}

function LlamaCppCard({ state }: { state: ReturnType<typeof useLiveResource<InferenceStatus | null>>['state'] }) {
  const { t } = useI18n()
  const inf = state.phase === 'ready' ? state.data : null

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Lightning size={16} className="text-accent" />
          llama.cpp
          <Badge variant="outline" className="ml-auto font-mono text-[10px]">GPU</Badge>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <LiveStates state={state} emptyLabel={t('inf.empty')} />
        {inf && (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline" className="font-mono text-[10px] gap-1"><Database size={11} />{inf.model}</Badge>
              <Badge variant="outline" className="font-mono text-[10px]">{t('inf.slots', { n: inf.slots })}</Badge>
            </div>
            <Meter label={t('ovi.kv')} pct={inf.kvUsePct} detail={`${inf.kvTokens} / ${inf.nCtx}`} />
            <div className="grid grid-cols-2 gap-2 text-[11px] font-mono">
              <Stat icon={<Gauge size={12} />} label={t('inf.prompt')} value={formatRate(inf.promptTokensPerSec)} />
              <Stat icon={<Gauge size={12} />} label={t('inf.decode')} value={formatRate(inf.predictedTokensPerSec)} />
              <Stat label={t('inf.processing')} value={`${inf.requestsProcessing}`} />
              <Stat label={t('inf.deferred')} value={`${inf.requestsDeferred}`} />
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function TeiCard({ state }: { state: ReturnType<typeof useLiveResource<TeiStatus | null>>['state'] }) {
  const { t } = useI18n()
  const tei = state.phase === 'ready' ? state.data : null
  const successPct = tei && tei.requestCount ? (tei.successCount / tei.requestCount) * 100 : 100

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Database size={16} className="text-muted-foreground" />
          TEI
          <Badge variant="outline" className="ml-auto font-mono text-[10px]">CPU</Badge>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <LiveStates state={state} emptyLabel={t('ovi.tei.empty')} />
        {tei && (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline" className="font-mono text-[10px] gap-1"><Database size={11} />{tei.model}</Badge>
              {tei.dtype && <Badge variant="outline" className="font-mono text-[10px]">{tei.dtype}</Badge>}
            </div>
            <Meter label={t('ovi.success')} pct={successPct} detail={`${tei.successCount} / ${tei.requestCount}`} />
            <div className="grid grid-cols-2 gap-2 text-[11px] font-mono">
              <Stat label={t('ovi.queue')} value={`${tei.queueSize}`} />
              <Stat label={t('ovi.avgInference')} value={`${tei.avgInferenceMs.toFixed(1)} ms`} />
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}


