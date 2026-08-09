import { BurstNode, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { TuningDashboard, EntityTile } from '@/components/TuningDashboard'
import { TrendRow } from '@/components/Sparkline'
import { inverseScoreTone } from '@/lib/thresholds'
import { HardDrive } from '@phosphor-icons/react'

const frame = createFrameClient()
const GiB = 1024 ** 3

const WINDOW_HOURS = 3

/**
 * A scratch tier is supposed to fill and drain. The level alone cannot tell a
 * healthy burst from a leak, so the second series tracks how fast it is moving:
 * a rising level with no drain is staged data nobody cleaned up.
 */
const TREND_QUERIES = [
  {
    metric: 'Staged',
    q: 'sum(node_filesystem_size_bytes{mountpoint="/burst-buffer"} - node_filesystem_avail_bytes{mountpoint="/burst-buffer"}) / 1024^3',
    unit: ' GiB',
  },
  {
    metric: 'Fill rate',
    q: 'sum(rate(node_filesystem_avail_bytes{mountpoint="/burst-buffer"}[10m])) * -1 / 1024^2',
    unit: ' MiB/s',
  },
]

export function BurstBufferView() {
  // node-exporter/Prometheus metrics, not objects — nothing to watch. A
  // scratch tier fills and drains over minutes, so 30s is plenty fresh.
  const { state, reload } = useLiveResource<BurstNode[]>(() => frame.cluster.burstBuffer(), [], [], 30_000)
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(
    () => frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
    [],
    [],
    30_000,
  )
  const nodes = state.phase === 'ready' ? state.data : []
  const trend = trendState.phase === 'ready' ? trendState.data : null

  const total = nodes.reduce((s, n) => s + n.totalBytes, 0)
  const used = nodes.reduce((s, n) => s + n.usedBytes, 0)
  const usedPct = total ? (used / total) * 100 : 0

  return (
    <TuningDashboard
      title="Burst Buffer — SSD scratch tier"
      icon={<HardDrive className="text-primary" />}
      state={state}
      onReload={reload}
      emptyLabel="No /burst-buffer mount found on any node."
      stats={[
        { label: 'Nodes with burst', value: `${nodes.length}` },
        { label: 'Total capacity', value: `${(total / GiB).toFixed(1)} GiB` },
        {
          label: 'Staged',
          value: `${(used / GiB).toFixed(2)} GiB`,
          // Scratch is meant to be used, so only a nearly-full tier is a problem
          // — that is when a job that needs to stage data starts failing.
          tone: inverseScoreTone(usedPct, 75, 90),
        },
      ]}
      trend={
        trend && (
          <>
            {trend.map((series) => (
              <TrendRow key={series.metric} series={series} windowHours={WINDOW_HOURS} format={(v) => v.toFixed(2)} />
            ))}
          </>
        )
      }
      entitiesTitle="Per-node"
      entities={nodes}
      entityKey={(n) => n.node}
      entityColumns={2}
      renderEntity={(n) => {
        const pct = n.totalBytes ? (n.usedBytes / n.totalBytes) * 100 : 0
        return (
          <EntityTile name={n.node}>
            <div className="space-y-1">
              <Progress value={pct} className="h-2" />
              <div className="flex justify-between text-[10px] text-muted-foreground font-mono">
                <span>{(n.usedBytes / GiB).toFixed(2)} GiB staged</span>
                <span>{(n.totalBytes / GiB).toFixed(1)} GiB SSD</span>
              </div>
            </div>
          </EntityTile>
        )
      }}
      configTitle="How the tier is mounted"
      config={`# node-prep.sh formats the ssd=1 disk and mounts it by UUID
mkfs.ext4 /dev/sdX
echo "UUID=<uuid> /burst-buffer ext4 defaults,nofail 0 2" >> /etc/fstab

# Read back from node-exporter as node_filesystem_*{mountpoint="/burst-buffer"}`}
    />
  )
}
