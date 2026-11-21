import { KsmStats, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Badge } from '@/components/ui/badge'
import { useLiveResource } from '@/hooks/useLiveResource'
import { TuningDashboard, EntityTile } from '@/components/TuningDashboard'
import { TrendRow } from '@/components/Sparkline'
import { scoreTone } from '@/lib/thresholds'
import { Archive } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import { richText } from '@/i18n/rich'
import type { MessageKey } from '@/i18n'

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

const TREND_LABEL: Record<string, MessageKey> = {
  'Pages sharing': 'ksm.trend.sharing',
  'Pages shared': 'ksm.trend.shared',
}

export function KsmView() {
  const { t, tn, locale } = useI18n()
  // node-exporter's own /metrics, not a Kubernetes object — nothing to watch.
  // Page-sharing counts move slowly, so this stays at the default 30s cadence.
  const { state, reload } = useLiveResource<KsmStats>(() => frame.cluster.ksm(), [], [], 30_000)
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(
    () => frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
    [],
    [],
    30_000,
  )
  const s = state.phase === 'ready' ? state.data : null
  const trend = trendState.phase === 'ready' ? trendState.data : null

  // A node with KSM off contributes nothing, so rate the feature on the nodes
  // that actually run it rather than on the fleet.
  const coverage = s && s.nodes.length > 0 ? (s.enabledNodes / s.nodes.length) * 100 : 0

  return (
    <TuningDashboard
      title={t('ksm.title')}
      icon={<Archive className="text-primary" />}
      state={state}
      onReload={reload}
      emptyLabel={t('ksm.empty')}
      stats={
        s
          ? [
              {
                label: t('ksm.stat.enabled'),
                value: `${s.enabledNodes} / ${s.nodes.length}`,
                tone: scoreTone(coverage, 100, 50),
              },
              { label: t('ksm.stat.saved'), value: `${s.totalSavedMiB.toFixed(1)} MiB` },
              { label: t('ksm.stat.sharing'), value: s.totalPagesSharing.toLocaleString(locale) },
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
                label={TREND_LABEL[series.metric] ? t(TREND_LABEL[series.metric]) : undefined}
                windowHours={WINDOW_HOURS}
                format={(v) => v.toLocaleString(locale, { maximumFractionDigits: 0 })}
              />
            ))}
          </>
        )
      }
      note={
        s && s.enabledNodes > 0 && s.totalPagesSharing === 0 ? (
          <p className="text-sm text-muted-foreground">
            {richText(tn('ksm.note', s.enabledNodes), {
              madvise: <span className="font-mono">madvise(MADV_MERGEABLE)</span>,
              prctl: <span className="font-mono">prctl(PR_SET_MEMORY_MERGE)</span>,
            })}
          </p>
        ) : undefined
      }
      entitiesTitle={t('ksm.perNode')}
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
              {n.run ? t('ksm.on') : t('ksm.off')}
            </Badge>
          }
          className={n.run ? undefined : 'opacity-60'}
        >
          <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground font-mono">
            <div>
              <div className="text-foreground font-bold">{n.savedMiB.toFixed(1)} MiB</div>
              {t('ksm.saved')}
            </div>
            <div>
              <div className="text-foreground font-bold">{n.pagesSharing.toLocaleString(locale)}</div>
              {t('ksm.sharing')}
            </div>
            <div>
              <div className="text-foreground font-bold">{n.fullScans}</div>
              {t('ksm.scans')}
            </div>
          </div>
        </EntityTile>
      )}
      configTitle={t('ksm.config')}
      config={`# node-prep.sh sets these per node, persisted by ksm-enable.service
echo 1    > /sys/kernel/mm/ksm/run
echo 1000 > /sys/kernel/mm/ksm/pages_to_scan
echo 200  > /sys/kernel/mm/ksm/sleep_millisecs

# Read back by node-exporter's ksmd collector as node_ksmd_*`}
    />
  )
}
