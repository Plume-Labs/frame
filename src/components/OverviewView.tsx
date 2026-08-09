import {
  AlertsStatus,
  BackupStatus,
  CephStatus,
  ClusterNodeInfo,
  InferenceStatus,
  MetricSeries,
  PostureStatus,
  coreListPath,
  createFrameClient,
  crdListPath,
} from '@/lib/frame-sdk'
import { config } from '@/lib/frame-config'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { useNavigation } from '@/hooks/useNavigation'
import { Stat, StatGrid, ScreenHeader } from '@/components/Primitives'
import { TrendRow } from '@/components/Sparkline'
import { TONE_TEXT, formatAge, inverseScoreTone } from '@/lib/thresholds'
import { Finding, collectFindings } from '@/lib/findings'
import { cn } from '@/lib/utils'
import {
  Bell,
  CheckCircle,
  Detective,
  Lightning,
  SquaresFour,
  TrendUp,
  Warning,
} from '@phosphor-icons/react'

const frame = createFrameClient()

const WINDOW_HOURS = 3

/** The four cluster-level series worth carrying on a landing page. */
const TREND_QUERIES = [
  { metric: 'CPU', q: '100 * (1 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m])))', unit: '%' },
  { metric: 'Memory', q: '100 * (1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes))', unit: '%' },
  { metric: 'GPU', q: 'avg(DCGM_FI_DEV_GPU_UTIL)', unit: '%' },
  { metric: 'Storage', q: '100 * ceph_cluster_total_used_bytes / ceph_cluster_total_bytes', unit: '%' },
]

/**
 * The landing screen.
 *
 * Every other screen answers one question well and none of them says whether
 * anything needs you right now — the app opened on Jobs, so finding a problem
 * meant knowing which screen to check first. This is that missing first read:
 * what is wrong, where the cluster is heading, and what it is serving.
 *
 * Its tiles navigate. A row saying two nodes are not ready is only useful if it
 * takes you to the nodes, which is why the screen state had to move into a
 * context before this could exist.
 */
export function OverviewView() {
  const cephNs = config().namespaces.ceph
  const veleroNs = config().namespaces.velero
  // Sourced from the same integration config ceph() itself reads osd/mon pods
  // from, so a Settings edit to either can never make the watch diverge from
  // the fetch. Both default to rook-ceph and usually match, hence the dedupe.
  const cephPodNamespaces = [...new Set([
    config().integrations.cephOsd.namespace,
    config().integrations.cephMon.namespace,
  ])]

  // A mix, call by call: some read real objects (watch), some read a proxied
  // metric with nothing to watch (poll).
  const nodes = useLiveResource<ClusterNodeInfo[]>(() => frame.cluster.nodes(), [], [coreListPath('nodes')])
  const alerts = useLiveResource<AlertsStatus | null>(() => frame.cluster.alerts(), [], [], 30_000)
  const ceph = useLiveResource<CephStatus>(() => frame.cluster.ceph(), [], [
    crdListPath('ceph.rook.io', 'v1', 'cephclusters', cephNs),
    crdListPath('ceph.rook.io', 'v1', 'cephblockpools', cephNs),
    ...cephPodNamespaces.map((ns) => coreListPath('pods', ns)),
  ])
  const backups = useLiveResource<BackupStatus | null>(() => frame.cluster.backups(), [], [
    crdListPath('velero.io', 'v1', 'backups', veleroNs),
    crdListPath('velero.io', 'v1', 'backupstoragelocations', veleroNs),
    crdListPath('velero.io', 'v1', 'schedules', veleroNs),
  ])
  const inference = useLiveResource<InferenceStatus | null>(() => frame.cluster.inference(), [], [], 10_000)
  const posture = useLiveResource<PostureStatus | null>(() => frame.cluster.posture(), [], [
    crdListPath('aquasecurity.github.io', 'v1alpha1', 'vulnerabilityreports'),
    crdListPath('aquasecurity.github.io', 'v1alpha1', 'configauditreports'),
  ])
  const trendState = useLiveResource<MetricSeries[] | null>(
    () => frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
    [],
    [],
    30_000,
  )

  const loading = [nodes, alerts, ceph, backups, inference, posture].some(
    (r) => r.state.phase === 'loading',
  )

  function reloadAll() {
    ;[nodes, alerts, ceph, backups, inference, posture, trendState].forEach((r) => r.reload())
  }

  return (
    <div className="space-y-6">
      <ScreenHeader
        icon={<SquaresFour className="text-primary" />}
        title="Overview"
        state={loading ? { phase: 'loading' } : { phase: 'ready', data: null }}
        onReload={reloadAll}
      />

      <AttentionCard nodes={nodes.state} alerts={alerts.state} ceph={ceph.state} backups={backups.state} />

      <TrendCard state={trendState.state} />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <ServingCard state={inference.state} />
        <PostureCard state={posture.state} backups={backups.state} />
      </div>
    </div>
  )
}

/** One actionable line: what is wrong, how bad, and where to go about it. */
function FindingRow({ finding }: { finding: Finding }) {
  const { navigate } = useNavigation()
  return (
    <button
      type="button"
      onClick={() => navigate(finding.screen, finding.tab)}
      className="w-full flex items-center gap-3 p-3 rounded-lg border border-border bg-secondary/30 hover:bg-secondary/60 transition-colors text-left"
    >
      <Warning size={16} className={cn('shrink-0', TONE_TEXT[finding.tone])} />
      <span className="font-mono text-xs font-bold">{finding.label}</span>
      <span className="text-[11px] text-muted-foreground truncate flex-1">{finding.detail}</span>
      <span className="text-[10px] font-mono text-muted-foreground shrink-0">open →</span>
    </button>
  )
}

function AttentionCard({
  nodes,
  alerts,
  ceph,
  backups,
}: {
  nodes: ReturnType<typeof useLiveResource<ClusterNodeInfo[]>>['state']
  alerts: ReturnType<typeof useLiveResource<AlertsStatus | null>>['state']
  ceph: ReturnType<typeof useLiveResource<CephStatus>>['state']
  backups: ReturnType<typeof useLiveResource<BackupStatus | null>>['state']
}) {
  const findings = collectFindings({
    nodes: nodes.phase === 'ready' ? nodes.data : undefined,
    alerts: alerts.phase === 'ready' ? alerts.data : undefined,
    ceph: ceph.phase === 'ready' ? ceph.data : undefined,
    backups: backups.phase === 'ready' ? backups.data : undefined,
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Bell size={16} className="text-primary" /> Needs attention
          {findings.length > 0 && (
            <Badge variant="outline" className="ml-auto font-mono text-[10px] text-warning border-warning/40">
              {findings.length}
            </Badge>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {findings.length === 0 ? (
          // Saying "nothing is wrong" is a result. An empty card reads as a
          // panel that failed to load.
          <div className="flex items-center gap-2 py-4 text-sm font-mono text-muted-foreground">
            <CheckCircle size={18} className="text-accent" />
            No firing alerts, every node ready, Ceph healthy, backups current.
          </div>
        ) : (
          findings.map((f) => <FindingRow key={f.key} finding={f} />)
        )}
      </CardContent>
    </Card>
  )
}

function TrendCard({ state }: { state: ReturnType<typeof useLiveResource<MetricSeries[] | null>>['state'] }) {
  const trend = state.phase === 'ready' ? state.data : null
  if (!trend) return null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <TrendUp size={16} className="text-primary" /> Cluster, last {WINDOW_HOURS}h
        </CardTitle>
      </CardHeader>
      <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6">
        {trend.map((series) => (
          <TrendRow
            key={series.metric}
            series={series}
            windowHours={WINDOW_HOURS}
            tone={inverseScoreTone(series.current, 75, 90)}
          />
        ))}
      </CardContent>
    </Card>
  )
}

function ServingCard({ state }: { state: ReturnType<typeof useLiveResource<InferenceStatus | null>>['state'] }) {
  const { navigate } = useNavigation()
  const inf = state.phase === 'ready' ? state.data : null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Lightning size={16} className="text-primary" /> Serving
          <button
            type="button"
            onClick={() => navigate('inference')}
            className="ml-auto text-[10px] font-mono text-muted-foreground hover:text-foreground"
          >
            open →
          </button>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {inf ? (
          <StatGrid count={4}>
            <Stat label="Decode" value={`${inf.predictedTokensPerSec.toFixed(1)}`} tone="accent" size="md" />
            <Stat label="KV used" value={`${inf.kvUsePct.toFixed(0)}%`} size="md" />
            <Stat label="Processing" value={`${inf.requestsProcessing}`} size="md" />
            <Stat
              label="Deferred"
              value={`${inf.requestsDeferred}`}
              size="md"
              tone={inf.requestsDeferred > 0 ? 'warning' : 'foreground'}
            />
          </StatGrid>
        ) : (
          <p className="text-sm text-muted-foreground font-mono">No inference server deployed.</p>
        )}
      </CardContent>
    </Card>
  )
}

function PostureCard({
  state,
  backups,
}: {
  state: ReturnType<typeof useLiveResource<PostureStatus | null>>['state']
  backups: ReturnType<typeof useLiveResource<BackupStatus | null>>['state']
}) {
  const { navigate } = useNavigation()
  const p = state.phase === 'ready' ? state.data : null
  const bk = backups.phase === 'ready' ? backups.data : null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Detective size={16} className="text-primary" /> Posture
          <button
            type="button"
            onClick={() => navigate('security')}
            className="ml-auto text-[10px] font-mono text-muted-foreground hover:text-foreground"
          >
            open →
          </button>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {p ? (
          <StatGrid count={4}>
            <Stat
              label="Critical vulns"
              value={p.vulns.critical}
              size="md"
              tone={p.vulns.critical > 0 ? 'destructive' : 'accent'}
            />
            <Stat label="High vulns" value={p.vulns.high} size="md" tone={p.vulns.high > 0 ? 'warning' : 'accent'} />
            <Stat label="Images scanned" value={p.vulns.images} size="md" />
            <Stat label="Misconfigs" value={p.misconfigs.critical + p.misconfigs.high} size="md" />
          </StatGrid>
        ) : (
          <p className="text-sm text-muted-foreground font-mono">trivy-operator not deployed.</p>
        )}
        {bk?.lastSuccess && (
          <div className="text-[11px] font-mono text-muted-foreground">
            last backup {formatAge(new Date(bk.lastSuccess).getTime())} ago
            {bk.schedule ? ` · schedule ${bk.schedule}` : ' · no schedule'}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
