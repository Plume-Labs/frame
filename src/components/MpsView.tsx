import { GpuInfo, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { TuningDashboard, EntityTile } from '@/components/TuningDashboard'
import { TrendRow } from '@/components/Sparkline'
import { Gauge } from '@phosphor-icons/react'

const frame = createFrameClient()

const WINDOW_HOURS = 3

/**
 * With MPS off there is no sharing telemetry to show, so this screen answers
 * the question that is actually live: what does exclusive mode cost here?
 *
 * Utilisation says whether the GPU is idle enough that a second tenant would
 * fit; framebuffer headroom says whether one could be admitted at all, since
 * MPS clients do not share device memory — each needs its own working set.
 */
const TREND_QUERIES = [
  { metric: 'GPU utilisation', q: 'avg(DCGM_FI_DEV_GPU_UTIL)', unit: '%' },
  { metric: 'Framebuffer used', q: 'sum(DCGM_FI_DEV_FB_USED)', unit: ' MiB' },
]

export function MpsView() {
  // DCGM's own /metrics, not a Kubernetes object — nothing to watch.
  // Utilisation is exactly the kind of second-to-second gauge that earns a
  // fast rate rather than the 30s+ most screens use.
  const { state, reload } = useLiveResource<GpuInfo[]>(() => frame.cluster.gpus(), [], [], 10_000)
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(
    () => frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
    [],
    [],
    30_000,
  )
  const gpus = state.phase === 'ready' ? state.data : []
  const trend = trendState.phase === 'ready' ? trendState.data : null

  const meanUtil = gpus.length ? gpus.reduce((s, g) => s + g.utilPct, 0) / gpus.length : 0
  const idleHeadroom = Math.max(0, 100 - meanUtil)

  return (
    <TuningDashboard
      title="MPS — GPU sharing"
      icon={<Gauge className="text-primary" />}
      badge={
        <Badge variant="outline" className="font-mono text-[10px]">
          exclusive · MPS off
        </Badge>
      }
      state={state}
      onReload={reload}
      emptyLabel="No GPUs present."
      stats={[
        { label: 'GPUs advertised', value: `${gpus.length} × exclusive` },
        { label: 'Clients per GPU', value: '1', size: 'sm' },
        { label: 'Mean utilisation', value: `${meanUtil.toFixed(0)}%` },
        { label: 'Idle headroom', value: `${idleHeadroom.toFixed(0)}%` },
      ]}
      trend={
        trend && (
          <>
            {trend.map((series) => (
              <TrendRow key={series.metric} series={series} windowHours={WINDOW_HOURS} />
            ))}
          </>
        )
      }
      note={
        <p className="text-sm text-muted-foreground">
          Multi-Process Service lets several processes share one GPU context. The device-plugin here
          advertises each GPU as a single exclusive resource, so a second workload cannot be admitted
          however idle the device is — the headroom above is the share that goes unused rather than
          being reclaimable. Enabling MPS or time-slicing trades that back for weaker isolation:
          clients share a context, so one of them faulting can take the others down with it.
        </p>
      }
      entitiesTitle="Per-GPU"
      entities={gpus}
      entityKey={(g) => `${g.node}-${g.index}`}
      entityColumns={2}
      renderEntity={(g) => (
        <EntityTile
          name={`${g.model} #${g.index}`}
          badge={
            <span className="text-[10px] font-mono text-muted-foreground truncate">{g.node}</span>
          }
        >
          <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground font-mono">
            <div>
              <div className="text-foreground font-bold">1 × nvidia.com/gpu</div>
              advertised
            </div>
            <div>
              <div className="text-foreground font-bold">1 (exclusive)</div>
              clients
            </div>
            <div>
              <div className="text-foreground font-bold">{g.utilPct}%</div>
              util now
            </div>
          </div>
        </EntityTile>
      )}
      configTitle="How sharing would be enabled"
      config={`# GPU operator device-plugin — time-slicing (coarse, no memory isolation)
sharing:
  timeSlicing:
    resources:
      - name: nvidia.com/gpu
        replicas: 4          # 1 physical GPU advertised as 4 schedulable units

# or MPS, which shares one context instead of interleaving whole time slices
sharing:
  mps:
    resources:
      - name: nvidia.com/gpu
        replicas: 4`}
    />
  )
}
