import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { LiveStates } from '@/components/LiveStates'
import { useLiveResource } from '@/hooks/useLiveResource'
import { useNow } from '@/hooks/useNow'
import { createFrameClient } from '@/lib/frame-sdk'
import type { Machine } from '@/lib/machines'
import { useI18n } from '@/i18n/context'
import { percentLabel, PodTraffic, rankLinks, rateLabel } from '@/lib/network-bottlenecks'

const frame = createFrameClient()

function LinksCard({ machines }: { machines: Machine[] }) {
  const { t } = useI18n()
  const now = useNow()
  const rows = rankLinks(machines, now)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg">{t('bneck.linksTitle')}</CardTitle>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        {rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('bneck.noRates')}</p>
        ) : (
          <table className="w-full text-xs font-mono">
            <thead className="text-left text-muted-foreground">
              <tr>
                <th className="pr-4 py-1">{t('bneck.col.link')}</th>
                <th className="pr-4">{t('bneck.col.now')}</th>
                <th className="pr-4">{t('bneck.col.5min')}</th>
                <th className="pr-4">{t('bneck.col.peak15')}</th>
                <th className="pr-4">{t('bneck.col.in')}</th>
                <th className="pr-4">{t('bneck.col.out')}</th>
                <th className="pr-4">{t('bneck.col.errors')}</th>
                <th>{t('bneck.col.drops')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.node}/${r.name}`} className={`border-t border-border ${r.stale ? 'text-muted-foreground' : ''}`}>
                  <td className="pr-4 py-1">
                    {r.node} {r.name}
                    {r.kind === 'member' && <span className="text-muted-foreground"> {t('bneck.member')}</span>}
                    {r.stale && <span className="text-warning"> {t('bneck.silent')}</span>}
                  </td>
                  <td className="pr-4">{percentLabel(r.traffic.utilizationPercent)}</td>
                  <td className="pr-4">{percentLabel(r.traffic.sustainedUtilizationPercent)}</td>
                  <td className="pr-4">{percentLabel(r.traffic.peakUtilizationPercent)}</td>
                  <td className="pr-4">{rateLabel(r.traffic.rxBytesPerSecond)}</td>
                  <td className="pr-4">{rateLabel(r.traffic.txBytesPerSecond)}</td>
                  <td className={`pr-4 ${r.traffic.errorsPerMinute > 0 ? 'text-warning' : ''}`}>{r.traffic.errorsPerMinute}</td>
                  <td className={r.traffic.dropsPerMinute > 0 ? 'text-warning' : ''}>{r.traffic.dropsPerMinute}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="mt-3 text-xs text-muted-foreground">
          {t('bneck.linksNote')}
        </p>
      </CardContent>
    </Card>
  )
}

function PodList({ title, pods }: { title: string; pods: PodTraffic[] | null }) {
  const { t } = useI18n()
  return (
    <div className="space-y-1">
      <h3 className="text-sm font-medium">{title}</h3>
      {pods === null && <p className="text-xs text-warning">{t('bneck.queryFailed')}</p>}
      {pods?.map((p) => (
        <div key={`${p.namespace}/${p.pod}`} className="flex justify-between gap-4 text-xs font-mono">
          <span className="truncate">
            {p.namespace}/{p.pod} <span className="text-muted-foreground">{t('bneck.on', { node: p.node })}</span>
          </span>
          <span>{rateLabel(p.bytesPerSecond)}</span>
        </div>
      ))}
    </div>
  )
}

function PodsCard() {
  const { t } = useI18n()
  const { state } = useLiveResource(() => frame.cluster.podTraffic(), [], [], 30_000)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg">{t('bneck.podsTitle')}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <LiveStates state={state} emptyLabel="" />
        {state.phase === 'ready' &&
          (state.data === null ? (
            <p className="text-sm text-muted-foreground">{t('bneck.noProm')}</p>
          ) : (
            <>
              <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
                <PodList title={t('bneck.sending')} pods={state.data.tx} />
                <PodList title={t('bneck.receiving')} pods={state.data.rx} />
              </div>
              <p className="text-xs text-muted-foreground">{t('bneck.podsNote')}</p>
            </>
          ))}
      </CardContent>
    </Card>
  )
}

/** Where the cluster's network is tight, measured. Nothing here limits anything. */
export function NetworkBottlenecksView() {
  const { t } = useI18n()
  const { state } = useLiveResource<Machine[]>(() => frame.machines.list(), [], [frame.machines.watchPath()], 30_000)
  return (
    <div className="space-y-6">
      <LiveStates state={state} emptyLabel={t('bneck.noMachines')} />
      {state.phase === 'ready' && <LinksCard machines={state.data} />}
      <PodsCard />
    </div>
  )
}
