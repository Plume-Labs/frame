import { KsmStats, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { TuningDashboard, EntityTile } from '@/components/TuningDashboard'
import { TrendRow } from '@/components/Sparkline'
import { scoreTone } from '@/lib/thresholds'
import { Archive } from '@phosphor-icons/react'

const frame = createFrameClient()

const WINDOW_HOURS = 3

/**
 * Pages *sharing* is the win (how many duplicates were collapsed onto a shared
 * page); pages *shared* is the cost (how many shared pages back them). The
 * ratio between them is the actual saving, which is why the trend tracks both.
 */
const TREND_QUERIES = [
  { metric: 'Pages sharing', q: 'sum(node_ksmd_pages_sharing)' },
  { metric: 'Pages shared', q: 'sum(node_ksmd_pages_shared)' },
]

export function KsmView() {
  const { state, reload } = useLiveResource<KsmStats>(() => frame.cluster.ksm())
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(() =>
    frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
  )
  const s = state.phase === 'ready' ? state.data : null
  const trend = trendState.phase === 'ready' ? trendState.data : null

  // A node with KSM off contributes nothing, so rate the feature on the nodes
  // that actually run it rather than on the fleet.
  const coverage = s && s.nodes.length > 0 ? (s.enabledNodes / s.nodes.length) * 100 : 0

  return (
    <TuningDashboard
      title="KSM — Kernel Same-page Merging"
      icon={<Archive className="text-primary" />}
      state={state}
      onReload={reload}
      emptyLabel="node-exporter (ksmd) not deployed."
      stats={
        s
          ? [
              {
                label: 'Enabled nodes',
                value: `${s.enabledNodes} / ${s.nodes.length}`,
                tone: scoreTone(coverage, 100, 50),
              },
              { label: 'Memory saved', value: `${s.totalSavedMiB.toFixed(1)} MiB` },
              { label: 'Pages sharing', value: s.totalPagesSharing.toLocaleString() },
            ]
          : []
      }
      trend={
        trend && (
          <>
            {trend.map((series) => (
              <TrendRow
                key={series.metric}
                series={series}
                windowHours={WINDOW_HOURS}
                format={(v) => v.toLocaleString(undefined, { maximumFractionDigits: 0 })}
              />
            ))}
          </>
        )
      }
      entitiesTitle="Per-node"
      entities={s?.nodes ?? []}
      entityKey={(n) => n.node}
      renderEntity={(n) => (
        <EntityTile
          name={n.node}
          badge={
            <Badge
              variant="outline"
              className={`text-[10px] font-mono ${n.run ? 'text-accent' : 'text-muted-foreground'}`}
            >
              KSM {n.run ? 'ON' : 'OFF'}
            </Badge>
          }
          className={n.run ? undefined : 'opacity-60'}
        >
          <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground font-mono">
            <div>
              <div className="text-foreground font-bold">{n.savedMiB.toFixed(1)} MiB</div>
              saved
            </div>
            <div>
              <div className="text-foreground font-bold">{n.pagesSharing.toLocaleString()}</div>
              sharing
            </div>
            <div>
              <div className="text-foreground font-bold">{n.fullScans}</div>
              scans
            </div>
          </div>
        </EntityTile>
      )}
      configTitle="How KSM is enabled"
      config={`# node-prep.sh sets these per node, persisted by ksm-enable.service
echo 1    > /sys/kernel/mm/ksm/run
echo 1000 > /sys/kernel/mm/ksm/pages_to_scan
echo 200  > /sys/kernel/mm/ksm/sleep_millisecs

# Read back by node-exporter's ksmd collector as node_ksmd_*`}
    />
  )
}
