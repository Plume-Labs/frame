import { GpuInfo, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { TuningDashboard, EntityTile } from '@/components/TuningDashboard'
import { TrendRow } from '@/components/Sparkline'
import { Gauge } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'

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

const TREND_LABEL: Record<string, MessageKey> = {
  'GPU utilisation': 'mps.trend.util',
  'Framebuffer used': 'mps.trend.fb',
}

export function MpsView() {
  const { t } = useI18n()
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
      title={t('mps.title')}
      icon={<Gauge className="text-primary" />}
      badge={
        <Badge variant="outline" className="font-mono text-[10px]">
          {t('mps.badge')}
        </Badge>
      }
      state={state}
      onReload={reload}
      emptyLabel={t('mps.empty')}
      stats={[
        { label: t('mps.stat.advertised'), value: t('mps.stat.exclusive', { count: gpus.length }) },
        { label: t('mps.stat.clients'), value: '1', size: 'sm' },
        { label: t('mps.stat.meanUtil'), value: `${meanUtil.toFixed(0)}%` },
        { label: t('mps.stat.idle'), value: `${idleHeadroom.toFixed(0)}%` },
      ]}
      trend={
        trend && (
          <>
            {trend.map((series) => (
              <TrendRow
                key={series.metric}
                series={series}
                label={TREND_LABEL[series.metric] ? t(TREND_LABEL[series.metric]) : undefined}
                windowHours={WINDOW_HOURS}
              />
            ))}
          </>
        )
      }
      note={
        <p className="text-sm text-muted-foreground">{t('mps.note')}</p>
      }
      entitiesTitle={t('mps.perGpu')}
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
              {t('mps.advertised')}
            </div>
            <div>
              <div className="text-foreground font-bold">{t('mps.exclusive')}</div>
              {t('mps.clients')}
            </div>
            <div>
              <div className="text-foreground font-bold">{g.utilPct}%</div>
              {t('mps.utilNow')}
            </div>
          </div>
        </EntityTile>
      )}
      configTitle={t('mps.config')}
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
