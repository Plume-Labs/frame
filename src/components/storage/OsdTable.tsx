import { Fragment, useState } from 'react'
import type { OsdRow } from '@/lib/ceph-diagnostic'
import { osdDiskLabel } from '@/lib/storage-labels'
import { formatIec, NOT_COLLECTED } from '@/lib/storage'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { HardDrives } from '@phosphor-icons/react'

const STATE_CLASS: Record<OsdRow['state'], string> = {
  'up-in': 'text-accent',
  down: 'text-destructive',
  out: 'text-warning',
  unstable: 'text-warning',
  unknown: 'text-muted-foreground',
}

function ms(v: number | null): string {
  return v === null ? '—' : `${v}`
}

function podCell(o: OsdRow): string {
  if (!o.pod) return 'no pod'
  const r = o.pod.restarts > 0 ? `, ${o.pod.restarts} restart(s)` : ''
  if (o.pod.phase !== 'Running') return `${o.pod.phase} init ${o.pod.initDone}/${o.pod.initTotal}${r}`
  return `${o.pod.ready ? 'Running' : 'Running, not ready'}${r}`
}

export function OsdTable({ osds }: { osds: OsdRow[] }) {
  const [open, setOpen] = useState<number | null>(null)
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <HardDrives className="text-primary" /> OSDs ({osds.length})
        </CardTitle>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        <table className="w-full text-xs font-mono">
          <thead className="text-muted-foreground text-left">
            <tr>
              <th className="py-1 pr-3">OSD</th>
              <th className="pr-3">class</th>
              <th className="pr-3">node</th>
              <th className="pr-3">state</th>
              <th className="pr-3 text-right">PG</th>
              <th className="pr-3 text-right">size</th>
              <th className="pr-3 text-right">%USE</th>
              <th className="pr-3 text-right">apply/commit ms</th>
              <th className="pr-3 text-right">slow</th>
              <th className="pr-3 text-right" title="Not exported by Prometheus">CRUSH wt</th>
              <th className="pr-3">disk</th>
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
                  <td className={`pr-3 ${STATE_CLASS[o.state]}`}>{o.state}</td>
                  <td className={`pr-3 text-right ${o.pgs === 0 ? 'text-warning' : ''}`}>{o.pgs ?? '—'}</td>
                  <td className="pr-3 text-right">{formatIec(o.bytesTotal)}</td>
                  <td className="pr-3 text-right">{o.usePct === null ? '—' : `${o.usePct.toFixed(1)} %`}</td>
                  <td className="pr-3 text-right">
                    {ms(o.applyMs)}/{ms(o.commitMs)}
                  </td>
                  <td className={`pr-3 text-right ${(o.slowOps ?? 0) + (o.bluestoreSlow ?? 0) > 0 ? 'text-warning' : ''}`}>
                    {o.slowOps ?? '—'}
                    {(o.bluestoreSlow ?? 0) > 0 ? ` (+${o.bluestoreSlow} BlueStore)` : ''}
                  </td>
                  <td className="pr-3 text-right text-muted-foreground">{o.crushWeight ?? '—'}</td>
                  <td className="pr-3 truncate max-w-[22rem]">{osdDiskLabel(o)}</td>
                  <td className={o.state === 'unstable' ? 'text-warning' : ''}>{podCell(o)}</td>
                </tr>
                {open === o.id && (
                  <tr className="bg-secondary/30">
                    <td colSpan={12} className="p-3 space-y-1">
                      <div>
                        up {String(o.up ?? '?')} · in {String(o.in ?? '?')} · used {formatIec(o.bytesUsed)} of{' '}
                        {formatIec(o.bytesTotal)}
                        {o.stateDetail ? ` · ${o.stateDetail}` : ''}
                      </div>
                      <div>
                        mgr device: {o.devices.join(', ') || '—'} · device id: {o.deviceIds.join(', ') || '—'}
                      </div>
                      {o.disk.kind === 'physical' && (
                        <div>
                          joined on serial {o.disk.serial}
                          {o.disk.osPath ? ` · agent sees it as ${o.disk.osPath}` : ''}
                          {o.disk.osPath && o.devices[0] && o.disk.osPath !== `/dev/${o.devices[0]}`
                            ? ' (kernel name differs from the mgr’s — disks were renumbered; the serial decides)'
                            : ''}
                          {o.disk.model ? ` · ${o.disk.model}` : ''}
                          {o.disk.hostConflict ? ' · WARNING: that machine is not this OSD’s node' : ''}
                          {o.disk.duplicateOn.length ? ` · DUPLICATE serial, listed by ${o.disk.duplicateOn.join(', ')}` : ''}
                        </div>
                      )}
                      <div className="text-muted-foreground">
                        CRUSH weight: {NOT_COLLECTED} (mgr API) · SMART: {NOT_COLLECTED}
                        {o.pod ? ` · pod ${o.pod.name}, started ${o.pod.startedAt ?? '—'}` : ''}
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
