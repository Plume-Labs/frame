import { GpuInfo, InferenceStatus, MetricSeries, TeiStatus, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Stat, Meter } from '@/components/Primitives'
import { TrendRow } from '@/components/Sparkline'
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
  { metric: 'Decode rate', q: 'llamacpp:predicted_tokens_seconds', unit: ' tok/s' },
  { metric: 'Requests processing', q: 'llamacpp:requests_processing' },
]

/**
 * Single-glance serving overview: GPU (DCGM) + llama.cpp (GPU-backed chat
 * completions) + TEI (CPU-only embeddings) side by side. The detailed
 * per-surface breakdowns still live on their own screens (GPU, KV-Cache).
 */
export function InferenceOverviewView() {
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
            Inference
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reloadAll} disabled={loading}>
              <ArrowClockwise className={loading ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-xs text-muted-foreground font-mono">
            GPU-backed serving (llama.cpp) and CPU-only embeddings (TEI) at a glance.
            Per-surface detail: GPU and KV-Cache screens.
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
              <TrendUp size={16} className="text-primary" /> Serving chain, last {WINDOW_HOURS}h (Prometheus)
            </CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-1 sm:grid-cols-3 gap-6">
            {trend.map((series) => (
              <TrendRow key={series.metric} series={series} windowHours={WINDOW_HOURS} format={(v) => v.toFixed(1)} />
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function GpuCard({ state }: { state: ReturnType<typeof useLiveResource<GpuInfo[]>>['state'] }) {
  const gpu = state.phase === 'ready' ? state.data[0] : undefined
  const memPct = gpu?.memTotalMB ? (gpu.memUsedMB / gpu.memTotalMB) * 100 : 0

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Speedometer size={16} className="text-primary" />
          GPU
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <LiveStates state={state} emptyLabel="No GPU / DCGM exporter not present." />
        {gpu && (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline" className="font-mono text-[10px]">{gpu.model} #{gpu.index}</Badge>
              <Badge variant="outline" className="font-mono text-[10px] gap-1"><Thermometer size={11} />{gpu.tempC}°C</Badge>
              <Badge variant="outline" className="font-mono text-[10px] gap-1"><Lightning size={11} />{gpu.powerW.toFixed(0)}W</Badge>
            </div>
            <Meter label="Utilisation" pct={gpu.utilPct} detail={`${gpu.utilPct}%`} />
            <Meter label="Memory" pct={memPct} detail={`${(gpu.memUsedMB / 1024).toFixed(1)} / ${(gpu.memTotalMB / 1024).toFixed(1)} GiB`} />
          </>
        )}
      </CardContent>
    </Card>
  )
}

function LlamaCppCard({ state }: { state: ReturnType<typeof useLiveResource<InferenceStatus | null>>['state'] }) {
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
        <LiveStates state={state} emptyLabel="No inference server (llama.cpp) deployed." />
        {inf && (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline" className="font-mono text-[10px] gap-1"><Database size={11} />{inf.model}</Badge>
              <Badge variant="outline" className="font-mono text-[10px]">{inf.slots} slots</Badge>
            </div>
            <Meter label="KV cache" pct={inf.kvUsePct} detail={`${inf.kvTokens} / ${inf.nCtx}`} />
            <div className="grid grid-cols-2 gap-2 text-[11px] font-mono">
              <Stat icon={<Gauge size={12} />} label="Prompt tok/s" value={inf.promptTokensPerSec.toFixed(1)} />
              <Stat icon={<Gauge size={12} />} label="Decode tok/s" value={inf.predictedTokensPerSec.toFixed(1)} />
              <Stat label="Processing" value={`${inf.requestsProcessing}`} />
              <Stat label="Deferred" value={`${inf.requestsDeferred}`} />
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function TeiCard({ state }: { state: ReturnType<typeof useLiveResource<TeiStatus | null>>['state'] }) {
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
        <LiveStates state={state} emptyLabel="No embeddings server (TEI) deployed." />
        {tei && (
          <>
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline" className="font-mono text-[10px] gap-1"><Database size={11} />{tei.model}</Badge>
              {tei.dtype && <Badge variant="outline" className="font-mono text-[10px]">{tei.dtype}</Badge>}
            </div>
            <Meter label="Success rate" pct={successPct} detail={`${tei.successCount} / ${tei.requestCount}`} />
            <div className="grid grid-cols-2 gap-2 text-[11px] font-mono">
              <Stat label="Queue" value={`${tei.queueSize}`} />
              <Stat label="Avg inference" value={`${tei.avgInferenceMs.toFixed(1)} ms`} />
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}


