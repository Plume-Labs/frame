import { Fragment, useState } from 'react'
import type { OsdRow } from '@/lib/ceph-diagnostic'
import { OSD_STATE_LABEL, osdDiskLabel } from '@/lib/storage-labels'
import { formatIec } from '@/lib/storage'
import { useI18n } from '@/i18n/context'
import type { Message } from '@/i18n'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { HardDrives } from '@phosphor-icons/react'

const STATE_CLASS: Record<OsdRow['state'], string> = {
  'up-in': 'text-accent',
  down: 'text-destructive',
  out: 'text-warning',
  unstable: 'text-warning',
  settling: 'text-primary',
  unknown: 'text-muted-foreground',
}

function ms(v: number | null): string {
  return v === null ? '—' : `${v}`
}

/** The pod column: the phase is Kubernetes' own word (raw), the rest is ours. */
function podCell(o: OsdRow): Message {
  if (!o.pod) return { key: 'osd.pod.none' }
  const restarts: Message | '' =
    o.pod.restarts > 0
      ? { key: 'osd.pod.restartsSuffix', params: { what: { key: 'osd.pod.restarts', params: { count: o.pod.restarts } } } }
      : ''
  if (o.pod.phase !== 'Running')
    return { key: 'osd.pod.initCell', params: { phase: o.pod.phase, done: o.pod.initDone, total: o.pod.initTotal, restarts } }
  return { key: o.pod.ready ? 'osd.pod.running' : 'osd.pod.runningNotReady', params: { restarts } }
}

export function OsdTable({ osds }: { osds: OsdRow[] }) {
  const { t, tm } = useI18n()
  const text = (x: string | Message) => (typeof x === 'string' ? x : tm(x))
  const [open, setOpen] = useState<number | null>(null)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <HardDrives className="text-primary" /> {t('osd.title', { count: osds.length })}
        </CardTitle>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        <table className="w-full text-xs font-mono">
          <thead className="text-muted-foreground text-left">
            <tr>
              <th className="py-1 pr-3">OSD</th>
              <th className="pr-3">{t('pool.col.class')}</th>
              <th className="pr-3">{t('osd.col.node')}</th>
              <th className="pr-3">{t('osd.col.state')}</th>
              <th className="pr-3 text-right">PG</th>
              <th className="pr-3 text-right">{t('osd.col.size')}</th>
              <th className="pr-3 text-right">%USE</th>
              <th className="pr-3 text-right">apply/commit ms</th>
              <th className="pr-3 text-right">{t('osd.col.slow')}</th>
              <th className="pr-3 text-right" title={t('osd.crushTitle')}>CRUSH wt</th>
              <th className="pr-3">{t('osd.col.disk')}</th>
              <th>pod</th>
            </tr>
          </thead>
          <tbody>
            {osds.map((o) => (
              <Fragment key={o.id}>
                <tr
                  className="border-t border-border cursor-pointer hover:bg-muted/50"
                  onClick={() => setOpen(open === o.id ? null : o.id)}
                >
                  <td className="py-1 pr-3 font-bold">{o.name}</td>
                  <td className="pr-3">{o.deviceClass || '—'}</td>
                  <td className="pr-3">{o.host || '—'}</td>
                  <td className={`pr-3 ${STATE_CLASS[o.state]}`}>{t(OSD_STATE_LABEL[o.state])}</td>
                  <td className={`pr-3 text-right ${o.pgs === 0 ? 'text-warning' : ''}`}>{o.pgs ?? '—'}</td>
                  <td className="pr-3 text-right">{formatIec(o.bytesTotal)}</td>
                  <td className="pr-3 text-right">{o.usePct === null ? '—' : `${o.usePct.toFixed(1)} %`}</td>
                  <td className="pr-3 text-right">
                    {ms(o.applyMs)}/{ms(o.commitMs)}
                  </td>
                  <td className={`pr-3 text-right ${(o.slowOps ?? 0) + (o.bluestoreSlow ?? 0) > 0 ? 'text-warning' : ''}`}>
                    {o.slowOps ?? '—'}
                    {(o.bluestoreSlow ?? 0) > 0 ? ` ${t('osd.bluestoreSlow', { count: o.bluestoreSlow ?? 0 })}` : ''}
                  </td>
                  <td
                    className={`pr-3 text-right ${o.crushWeight === null ? 'text-muted-foreground' : o.crushWeight === 0 ? 'text-warning' : ''}`}
                  >
                    {o.crushWeight === null ? '—' : o.crushWeight.toFixed(2)}
                  </td>
                  <td className="pr-3 truncate max-w-[22rem]">{tm(osdDiskLabel(o))}</td>
                  <td className={o.state === 'unstable' ? 'text-warning' : ''}>{tm(podCell(o))}</td>
                </tr>
                {open === o.id && (
                  <tr className="bg-secondary/30">
                    <td colSpan={12} className="p-3 space-y-1">
                      <div>
                        {t('osd.detail.line', {
                          up: String(o.up ?? '?'),
                          in: String(o.in ?? '?'),
                          used: formatIec(o.bytesUsed),
                          total: formatIec(o.bytesTotal),
                        })}
                        {o.stateDetail ? ` · ${text(o.stateDetail)}` : ''}
                      </div>
                      <div>
                        {t('osd.detail.devices', { devices: o.devices.join(', ') || '—', ids: o.deviceIds.join(', ') || '—' })}
                      </div>
                      {o.disk.kind === 'physical' && (
                        <div>
                          {t('osd.detail.joined', { serial: o.disk.serial })}
                          {o.disk.osPath ? ` · ${t('osd.detail.agentSees', { path: o.disk.osPath })}` : ''}
                          {o.disk.osPath && o.devices[0] && o.disk.osPath !== `/dev/${o.devices[0]}`
                            ? ` ${t('osd.detail.renumbered')}`
                            : ''}
                          {o.disk.model ? ` · ${o.disk.model}` : ''}
                          {o.disk.hostConflict ? ` · ${t('osd.detail.hostConflict')}` : ''}
                          {o.disk.duplicateOn.length ? ` · ${t('osd.detail.duplicate', { list: o.disk.duplicateOn.join(', ') })}` : ''}
                        </div>
                      )}
                      <div className="text-muted-foreground">
                        {t('osd.detail.weightSmart', {
                          weight: o.crushWeight === null ? t('osd.weightNotCollected') : o.crushWeight,
                          smart: t('stv.notCollected'),
                        })}
                        {o.pod ? ` · ${t('osd.detail.pod', { name: o.pod.name, at: o.pod.startedAt ?? '—' })}` : ''}
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
      </CardContent>
    </Card>
  )
}
