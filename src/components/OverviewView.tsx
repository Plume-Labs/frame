import {
  AlertsStatus,
  BackupStatus,
  CephStatus,
  ClusterNodeInfo,
  GpuInfo,
  InferenceStatus,
  MetricSeries,
  PostureStatus,
  RegistryAlert,
  alertRegistryPath,
  coreListPath,
  createFrameClient,
  crdListPath,
} from '@/lib/frame-sdk'
import { config } from '@/lib/frame-config'
import { formatRate } from '@/lib/inference-rates'
import { formatTops, summarizeTops } from '@/lib/tops'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { useNavigation } from '@/hooks/useNavigation'
import { Stat, StatGrid, ScreenHeader } from '@/components/Primitives'
import { TrendRow } from '@/components/Sparkline'
import { TONE_TEXT, formatAge, inverseScoreTone } from '@/lib/thresholds'
import { Finding, collectFindings, unreadSources } from '@/lib/findings'
import { StaleNotice } from '@/components/LiveStates'
import type { Cache } from '@/lib/caches'
import type { Database } from '@/lib/databases'
import { volumeFillFindings, type VolumeLoadSnapshot } from '@/lib/volume-load'
import type { Machine } from '@/lib/machines'
import { cn } from '@/lib/utils'
import { useI18n } from '@/i18n/context'
import type { Message, MessageKey } from '@/i18n'
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

/** Series names come from the queries below (and the SDK echoes them back); the screen shows them translated. */
const TREND_LABEL: Record<string, MessageKey> = {
  CPU: 'ov.trend.cpu',
  Memory: 'ov.trend.memory',
  GPU: 'ov.trend.gpu',
  Storage: 'ov.trend.storage',
}

/** The source ids `collectFindings` / `unreadSources` use, named for people. */
const SOURCE_LABEL: Record<string, MessageKey> = {
  nodes: 'ov.src.nodes',
  alerts: 'ov.src.alerts',
  registry: 'ov.src.registry',
  ceph: 'ov.src.ceph',
  backups: 'ov.src.backups',
  machines: 'ov.src.machines',
  caches: 'ov.src.caches',
  databases: 'ov.src.databases',
  volumes: 'ov.src.volumes',
}

/** The Needs-attention sources keep their last read through a failed refresh and say so. */
const KEEP_STALE = { keepStaleOnError: true }

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
  const { t } = useI18n()
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
  const nodes = useLiveResource<ClusterNodeInfo[]>(
    () => frame.cluster.nodes(),
    [],
    [coreListPath('nodes')],
    undefined,
    KEEP_STALE,
  )
  const alerts = useLiveResource<AlertsStatus | null>(
    () => frame.cluster.alerts(),
    [],
    [],
    30_000,
    KEEP_STALE,
  )
  const registry = useLiveResource<RegistryAlert[]>(
    () => frame.cluster.alertRegistry(),
    [],
    [alertRegistryPath()],
    undefined,
    KEEP_STALE,
  )
  const ceph = useLiveResource<CephStatus>(() => frame.cluster.ceph(), [], [
    crdListPath('ceph.rook.io', 'v1', 'cephclusters', cephNs),
    crdListPath('ceph.rook.io', 'v1', 'cephblockpools', cephNs),
    ...cephPodNamespaces.map((ns) => coreListPath('pods', ns)),
  ], undefined, KEEP_STALE)
  const backups = useLiveResource<BackupStatus | null>(() => frame.cluster.backups(), [], [
    crdListPath('velero.io', 'v1', 'backups', veleroNs),
    crdListPath('velero.io', 'v1', 'backupstoragelocations', veleroNs),
    crdListPath('velero.io', 'v1', 'schedules', veleroNs),
  ], undefined, KEEP_STALE)
  // Same reads, same watch paths as the Health, Links, Caches and Databases
  // tabs, so a condition flipping there flips here at the same moment.
  const machines = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000, KEEP_STALE)
  const caches = useLiveResource<Cache[]>(() => frame.caches.list(), [], [frame.caches.watchPath()], undefined, KEEP_STALE)
  const databases = useLiveResource<Database[]>(() => frame.databases.list(), [], [frame.databases.watchPath()], undefined, KEEP_STALE)
  const volumeFill = useLiveResource<Pick<VolumeLoadSnapshot, 'volUsed' | 'volCapacity'> | null>(
    () => frame.cluster.volumeFill(),
    [],
    [],
    60_000,
    KEEP_STALE,
  )
  const inference = useLiveResource<InferenceStatus | null>(() => frame.cluster.inference(), [], [], 10_000)
  const gpus = useLiveResource<GpuInfo[]>(() => frame.cluster.gpus(), [], [], 10_000)
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
    ;[nodes, alerts, registry, ceph, backups, inference, posture, gpus, trendState].forEach((r) => r.reload())
  }

  return (
    <div className="space-y-6">
      <ScreenHeader
        icon={<SquaresFour className="text-primary" />}
        title={t('tab.overview')}
        state={loading ? { phase: 'loading' } : { phase: 'ready', data: null }}
        onReload={reloadAll}
      />

      <AttentionCard
        nodes={nodes.state}
        alerts={alerts.state}
        registry={registry.state}
        ceph={ceph.state}
        backups={backups.state}
        machines={machines.state}
        caches={caches.state}
        databases={databases.state}
        volumeFill={volumeFill.state}
      />

      <ComputeCard state={gpus.state} />

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
  const { t, tm } = useI18n()
  const { navigate } = useNavigation()
  const text = (x: string | Message) => (typeof x === 'string' ? x : tm(x))
  return (
    <button
      type="button"
      onClick={() => navigate(finding.screen, finding.tab)}
      className="w-full flex items-center gap-3 p-3 rounded-lg border border-border bg-secondary/30 hover:bg-secondary/60 transition-colors text-left"
    >
      <Warning size={16} className={cn('shrink-0', TONE_TEXT[finding.tone])} />
      <span className="font-mono text-xs font-bold">{text(finding.label)}</span>
      <span className="text-[11px] text-muted-foreground truncate flex-1">{text(finding.detail)}</span>
      <span className="text-[10px] font-mono text-muted-foreground shrink-0">{t('ov.open')}</span>
    </button>
  )
}

function AttentionCard({
  nodes,
  alerts,
  registry,
  ceph,
  backups,
  machines,
  caches,
  databases,
  volumeFill,
}: {
  nodes: ReturnType<typeof useLiveResource<ClusterNodeInfo[]>>['state']
  alerts: ReturnType<typeof useLiveResource<AlertsStatus | null>>['state']
  registry: ReturnType<typeof useLiveResource<RegistryAlert[]>>['state']
  ceph: ReturnType<typeof useLiveResource<CephStatus>>['state']
  backups: ReturnType<typeof useLiveResource<BackupStatus | null>>['state']
  machines: ReturnType<typeof useLiveResource<Machine[]>>['state']
  caches: ReturnType<typeof useLiveResource<Cache[]>>['state']
  databases: ReturnType<typeof useLiveResource<Database[]>>['state']
  volumeFill: ReturnType<typeof useLiveResource<Pick<VolumeLoadSnapshot, 'volUsed' | 'volCapacity'> | null>>['state']
}) {
  const { t } = useI18n()
  const findings = collectFindings({
    nodes: nodes.phase === 'ready' ? nodes.data : undefined,
    alerts: alerts.phase === 'ready' ? alerts.data : undefined,
    registry: registry.phase === 'ready' ? registry.data : undefined,
    ceph: ceph.phase === 'ready' ? ceph.data : undefined,
    backups: backups.phase === 'ready' ? backups.data : undefined,
    machines: machines.phase === 'ready' ? machines.data : undefined,
    caches: caches.phase === 'ready' ? caches.data : undefined,
    databases: databases.phase === 'ready' ? databases.data : undefined,
    volumes: volumeFill.phase === 'ready' && volumeFill.data ? volumeFillFindings(volumeFill.data) : undefined,
  })

  const unread = unreadSources({ nodes, alerts, registry, ceph, backups, machines, caches, databases, volumes: volumeFill })
  const unreadCount = unread.loading.length + unread.failed.length + unread.stale.length
  const names = (ids: string[]) => ids.map((id) => (SOURCE_LABEL[id] ? t(SOURCE_LABEL[id]) : id)).join(', ')
  const unreadNote = [
    unread.loading.length > 0 ? t('ov.unread.loading', { list: names(unread.loading) }) : '',
    unread.failed.length > 0 ? t('ov.unread.failed', { list: names(unread.failed) }) : '',
    unread.stale.length > 0 ? t('ov.unread.stale', { list: names(unread.stale) }) : '',
  ]
    .filter(Boolean)
    .join(' · ')

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Bell size={16} className="text-primary" /> {t('ov.attention')}
          {findings.length > 0 && (
            <Badge variant="outline" className="ml-auto font-mono text-[10px] text-warning border-warning/40">
              {findings.length}
            </Badge>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {Object.entries({ nodes, alerts, registry, ceph, backups, machines, caches, databases, volumes: volumeFill }).map(
          ([name, st]) => (
            <StaleNotice key={name} state={st} label={SOURCE_LABEL[name] ? t(SOURCE_LABEL[name]) : name} />
          ),
        )}
        {findings.length === 0 && unreadCount > 0 ? (
          // No findings from sources nobody has read is not "all clear": say
          // what is missing instead of showing a green check.
          <div className="flex items-center gap-2 py-4 text-sm font-mono text-muted-foreground">
            <Warning size={18} aria-hidden="true" className="shrink-0 text-warning" />
            {t('ov.cannotSay', { note: unreadNote })}
          </div>
        ) : findings.length === 0 ? (
          // Saying "nothing is wrong" is a result. An empty card reads as a
          // panel that failed to load.
          <div className="flex items-center gap-2 py-4 text-sm font-mono text-muted-foreground">
            <CheckCircle size={18} aria-hidden="true" className="text-accent" />
            <span>
              {t('ov.allClear')}
              {unread.unavailable.length > 0 && (
                <span className="block text-[11px]">{t('ov.notMonitored', { list: names(unread.unavailable) })}</span>
              )}
            </span>
          </div>
        ) : (
          findings.map((f) => <FindingRow key={f.key} finding={f} />)
        )}
        {findings.length > 0 && unreadCount > 0 && (
          <div className="text-[11px] font-mono text-muted-foreground">{t('ov.incomplete', { note: unreadNote })}</div>
        )}
      </CardContent>
    </Card>
  )
}

function TrendCard({ state }: { state: ReturnType<typeof useLiveResource<MetricSeries[] | null>>['state'] }) {
  const { t } = useI18n()
  const trend = state.phase === 'ready' ? state.data : null
  if (!trend) return null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <TrendUp size={16} className="text-primary" /> {t('ov.cluster', { hours: WINDOW_HOURS })}
        </CardTitle>
      </CardHeader>
      <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-6">
        {trend.map((series) => (
          <TrendRow
            key={series.metric}
            series={series}
            label={TREND_LABEL[series.metric] ? t(TREND_LABEL[series.metric]) : undefined}
            windowHours={WINDOW_HOURS}
            tone={inverseScoreTone(series.current, 75, 90)}
          />
        ))}
      </CardContent>
    </Card>
  )
}

/**
 * Compute capacity in TOPS. Hidden when the cluster has no GPU telemetry (no
 * DCGM, no cards): a tile saying "0 TOPS" would read as a fault.
 */
function ComputeCard({ state }: { state: ReturnType<typeof useLiveResource<GpuInfo[]>>['state'] }) {
  const { t, tn } = useI18n()
  const { navigate } = useNavigation()
  const gpus = state.phase === 'ready' ? state.data : []
  if (gpus.length === 0) return null
  const tops = summarizeTops(gpus)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Lightning size={16} className="text-primary" /> {t('nav.group.compute')}
          <button
            type="button"
            onClick={() => navigate('gpu')}
            className="ml-auto text-[10px] font-mono text-muted-foreground hover:text-foreground"
          >
            {t('ov.open')}
          </button>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        <StatGrid count={3}>
          <Stat label={t('ov.compute.peak')} value={tops.known ? formatTops(tops.peak) : '—'} tone="accent" size="md" />
          <Stat label={t('ov.compute.used')} value={tops.known ? formatTops(tops.used) : '—'} size="md" />
          <Stat label={t('ov.compute.gpus')} value={`${gpus.length}`} size="md" />
        </StatGrid>
        <p className="text-[11px] font-mono text-muted-foreground">
          {t('ov.compute.note')}
          {tops.unknown > 0 && ` ${tn('ov.compute.unknown', tops.unknown)}`}
        </p>
      </CardContent>
    </Card>
  )
}

function ServingCard({ state }: { state: ReturnType<typeof useLiveResource<InferenceStatus | null>>['state'] }) {
  const { t } = useI18n()
  const { navigate } = useNavigation()
  const inf = state.phase === 'ready' ? state.data : null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Lightning size={16} className="text-primary" /> {t('ov.serving')}
          <button
            type="button"
            onClick={() => navigate('inference')}
            className="ml-auto text-[10px] font-mono text-muted-foreground hover:text-foreground"
          >
            {t('ov.open')}
          </button>
        </CardTitle>
      </CardHeader>
      <CardContent>
        {inf ? (
          <StatGrid count={4}>
            <Stat label={t('ov.serving.decode')} value={formatRate(inf.predictedTokensPerSec)} tone="accent" size="md" />
            <Stat label={t('ov.serving.kv')} value={`${inf.kvUsePct.toFixed(0)}%`} size="md" />
            <Stat label={t('ov.serving.processing')} value={`${inf.requestsProcessing}`} size="md" />
            <Stat
              label={t('ov.serving.deferred')}
              value={`${inf.requestsDeferred}`}
              size="md"
              tone={inf.requestsDeferred > 0 ? 'warning' : 'foreground'}
            />
          </StatGrid>
        ) : (
          <p className="text-sm text-muted-foreground font-mono">
            {state.phase === 'loading'
              ? t('ov.reading')
              : state.phase === 'error'
                ? t('ov.serving.error')
                : t('ov.serving.none')}
          </p>
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
  const { t } = useI18n()
  const { navigate } = useNavigation()
  const p = state.phase === 'ready' ? state.data : null
  const bk = backups.phase === 'ready' ? backups.data : null
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Detective size={16} className="text-primary" /> {t('ov.posture')}
          <button
            type="button"
            onClick={() => navigate('security')}
            className="ml-auto text-[10px] font-mono text-muted-foreground hover:text-foreground"
          >
            {t('ov.open')}
          </button>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {p ? (
          <StatGrid count={4}>
            <Stat
              label={t('ov.posture.critical')}
              value={p.vulns.critical}
              size="md"
              tone={p.vulns.critical > 0 ? 'destructive' : 'accent'}
            />
            <Stat label={t('ov.posture.high')} value={p.vulns.high} size="md" tone={p.vulns.high > 0 ? 'warning' : 'accent'} />
            <Stat label={t('ov.posture.images')} value={p.vulns.images} size="md" />
            <Stat label={t('ov.posture.misconfigs')} value={p.misconfigs.critical + p.misconfigs.high} size="md" />
          </StatGrid>
        ) : (
          <p className="text-sm text-muted-foreground font-mono">
            {state.phase === 'loading'
              ? t('ov.reading')
              : state.phase === 'error'
                ? t('ov.posture.error')
                : t('ov.posture.none')}
          </p>
        )}
        {bk?.lastSuccess && (
          <div className="text-[11px] font-mono text-muted-foreground">
            {t('ov.posture.lastBackup', { age: formatAge(new Date(bk.lastSuccess).getTime()) })}
            {bk.schedule ? ` · ${t('ov.posture.schedule', { schedule: bk.schedule })}` : ` · ${t('ov.posture.noSchedule')}`}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
