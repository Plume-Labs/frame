import { PtpNode, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { TuningDashboard, EntityTile } from '@/components/TuningDashboard'
import { TrendRow } from '@/components/Sparkline'
import { inverseScoreTone, scoreTone } from '@/lib/thresholds'
import { Clock } from '@phosphor-icons/react'

const frame = createFrameClient()

const usFmt = (s: number) => `${(s * 1e6).toFixed(2)} µs`

const WINDOW_HOURS = 3

/**
 * Offset is the symptom, frequency adjustment is the cause: a clock held on
 * target by an ever-growing correction is drifting underneath, and only the
 * second series shows it. Both are absolute — sign says which way, not how bad.
 */
const TREND_QUERIES = [
  { metric: 'Max offset', q: 'max(abs(node_timex_offset_seconds)) * 1e6', unit: ' µs' },
  { metric: 'Max freq adjustment', q: 'max(abs(node_timex_frequency_adjustment_ratio - 1)) * 1e6', unit: ' ppm' },
]

// A clock inside 100 µs is fine for scheduling and log correlation; past 1 ms
// ordering across nodes stops being trustworthy.
const OFFSET_GOOD_S = 100e-6
const OFFSET_OK_S = 1e-3

export function PtpView() {
  // Both read node-exporter/Prometheus metrics, not Kubernetes objects — there
  // is nothing to watch. Clock offset drifts over minutes, not seconds.
  const { state, reload } = useLiveResource<PtpNode[]>(() => frame.cluster.ptp(), [], [], 30_000)
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(
    () => frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
    [],
    [],
    30_000,
  )
  const nodes = state.phase === 'ready' ? state.data : []
  const trend = trendState.phase === 'ready' ? trendState.data : null

  const synced = nodes.filter((n) => n.synced).length
  const maxOff = nodes.reduce((m, n) => Math.max(m, Math.abs(n.offsetSeconds)), 0)
  const syncedPct = nodes.length > 0 ? (synced / nodes.length) * 100 : 0

  return (
    <TuningDashboard
      title="Clock Sync — PTP / adjtimex"
      icon={<Clock className="text-primary" />}
      state={state}
      onReload={reload}
      emptyLabel="node-exporter (timex) not deployed."
      stats={[
        { label: 'Synced nodes', value: `${synced} / ${nodes.length}`, tone: scoreTone(syncedPct, 100, 50) },
        { label: 'Max offset', value: usFmt(maxOff), tone: inverseScoreTone(maxOff, OFFSET_GOOD_S, OFFSET_OK_S) },
        { label: 'Source', value: 'ptp_kvm PHC', size: 'sm' },
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
      renderEntity={(n) => (
        <EntityTile
          name={n.node}
          badge={
            <Badge
              variant="outline"
              className={`text-[10px] font-mono ${n.synced ? 'text-accent' : 'text-destructive'}`}
            >
              {n.synced ? 'SYNCED' : 'UNSYNCED'}
            </Badge>
          }
        >
          <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground font-mono">
            <div>
              <div className="text-foreground font-bold">{usFmt(n.offsetSeconds)}</div>
              offset
            </div>
            <div>
              <div className="text-foreground font-bold">{n.freqPpm.toFixed(1)}</div>
              ppm adj
            </div>
            <div>
              <div className="text-foreground font-bold">{usFmt(n.maxErrorSeconds)}</div>
              max err
            </div>
          </div>
        </EntityTile>
      )}
      configTitle="How the clock is disciplined"
      config={`# node-prep.sh loads the paravirtual PHC, persisted across reboots
modprobe ptp_kvm                       # -> /dev/ptp0, fed by the hypervisor
echo ptp_kvm > /etc/modules-load.d/ptp_kvm.conf

# chrony/systemd-timesyncd discipline the system clock from it; node-exporter's
# timex collector reports the result as node_timex_* (offset, maxerror, freq).`}
    />
  )
}
