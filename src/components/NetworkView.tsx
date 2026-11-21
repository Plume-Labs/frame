import { NetNode, MetricSeries, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Stat } from '@/components/Primitives'
import { TrendRow } from '@/components/Sparkline'
import { Network, ArrowClockwise, WarningCircle, TrendUp } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'

const frame = createFrameClient()

const WINDOW_HOURS = 3

/**
 * The tiles below count bytes since each node booted, which says how much has
 * ever moved and nothing about now — a number that only ever goes up cannot
 * show a spike or a stall. These are the same counters as rates, which is the
 * form you can actually read traffic from.
 */
const TREND_QUERIES = [
  { metric: 'Receive', q: 'sum(rate(node_network_receive_bytes_total{device="eth0"}[5m])) / 1024^2', unit: ' MiB/s' },
  { metric: 'Transmit', q: 'sum(rate(node_network_transmit_bytes_total{device="eth0"}[5m])) / 1024^2', unit: ' MiB/s' },
  {
    metric: 'Errors + drops',
    q: 'sum(rate(node_network_receive_errs_total[5m])) + sum(rate(node_network_transmit_errs_total[5m])) + sum(rate(node_network_receive_drop_total[5m])) + sum(rate(node_network_transmit_drop_total[5m]))',
    unit: '/s',
  },
]

const GiB = 1024 ** 3
const MiB = 1024 ** 2
const fmt = (b: number) => (b >= GiB ? `${(b / GiB).toFixed(2)} GiB` : `${(b / MiB).toFixed(0)} MiB`)
const pkts = (n: number) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${(n / 1e3).toFixed(0)}k` : `${n}`)

const IFACE_ROLE: Record<string, MessageKey> = {
  eth0: 'net.role.eth0',
  'flannel.1': 'net.role.flannel',
  cni0: 'net.role.cni0',
}

const TREND_LABEL: Record<string, MessageKey> = {
  Receive: 'net.trend.rx',
  Transmit: 'net.trend.tx',
  'Errors + drops': 'net.trend.errs',
}

export function NetworkView() {
  const { t } = useI18n()
  // node-exporter's own /metrics, not a Kubernetes object — nothing to watch.
  const { state, reload } = useLiveResource<NetNode[]>(() => frame.cluster.network(), [], [], 30_000)
  const { state: trendState } = useLiveResource<MetricSeries[] | null>(
    () => frame.cluster.range(TREND_QUERIES, WINDOW_HOURS),
    [],
    [],
    30_000,
  )
  const nodes = state.phase === 'ready' ? state.data : []
  const trend = trendState.phase === 'ready' ? trendState.data : null

  const flat = nodes.flatMap((n) => n.ifaces)
  const rx = flat.filter((i) => i.device === 'eth0').reduce((s, i) => s + i.rxBytes, 0)
  const tx = flat.filter((i) => i.device === 'eth0').reduce((s, i) => s + i.txBytes, 0)
  const errs = flat.reduce((s, i) => s + i.rxErrs + i.txErrs, 0)
  const drops = flat.reduce((s, i) => s + i.rxDrop + i.txDrop, 0)

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Network className="text-primary" />
            {t('net.title')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && (
          <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <Stat label={t('net.stat.rx')} value={fmt(rx)} />
            <Stat label={t('net.stat.tx')} value={fmt(tx)} />
            <Stat label={t('net.stat.errs')} value={`${errs}`} tone={errs ? 'warning' : 'accent'} />
            <Stat label={t('net.stat.drops')} value={`${drops}`} tone={drops ? 'warning' : 'accent'} />
          </CardContent>
        )}
      </Card>

      {trend && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-base flex items-center gap-2">
              <TrendUp size={16} className="text-primary" /> {t('net.trendTitle', { hours: WINDOW_HOURS })}
            </CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-1 sm:grid-cols-3 gap-6">
            {trend.map((series) => (
              <TrendRow
                key={series.metric}
                series={series}
                label={TREND_LABEL[series.metric] ? t(TREND_LABEL[series.metric]) : undefined}
                windowHours={WINDOW_HOURS}
                format={(v) => (v >= 1 ? v.toFixed(1) : v.toFixed(3))}
                tone={series.metric === 'Errors + drops' && series.current > 0 ? 'warning' : undefined}
              />
            ))}
          </CardContent>
        </Card>
      )}

      <LiveStates state={state} emptyLabel={t('net.empty')} />

      {state.phase === 'ready' &&
        nodes.map((n) => (
          <Card key={n.node}>
            <CardHeader>
              <CardTitle className="font-mono text-lg">{n.node}</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2">
              {n.ifaces.map((i) => {
                const bad = i.rxErrs + i.txErrs + i.rxDrop + i.txDrop
                return (
                  <div
                    key={i.device}
                    className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono text-xs font-bold">
                        {i.device}
                        <span className="text-muted-foreground font-normal ml-2">
                          {IFACE_ROLE[i.device] ? t(IFACE_ROLE[i.device]) : ''}
                        </span>
                      </span>
                      {bad > 0 && (
                        <Badge variant="outline" className="text-[10px] font-mono text-warning border-warning/40 gap-1">
                          <WarningCircle size={11} />
                          {t('net.errDrop', { count: bad })}
                        </Badge>
                      )}
                    </div>
                    <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-[10px] text-muted-foreground font-mono">
                      <Micro v={fmt(i.rxBytes)} l={t('net.rxBytes')} tone="accent" />
                      <Micro v={fmt(i.txBytes)} l={t('net.txBytes')} tone="primary" />
                      <Micro v={pkts(i.rxPackets)} l={t('net.rxPkts')} />
                      <Micro v={pkts(i.txPackets)} l={t('net.txPkts')} />
                    </div>
                  </div>
                )
              })}
            </CardContent>
          </Card>
        ))}

      <Badge variant="outline" className="font-mono text-[10px] text-muted-foreground">
        {t('net.footer')}
      </Badge>
    </div>
  )
}


function Micro({ v, l, tone }: { v: string; l: string; tone?: 'accent' | 'primary' }) {
  const c = tone === 'accent' ? 'text-accent' : tone === 'primary' ? 'text-primary' : 'text-foreground'
  return (
    <div>
      <div className={`font-bold ${c}`}>{v}</div>
      {l}
    </div>
  )
}
