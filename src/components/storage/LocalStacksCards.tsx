import type { Machine } from '@/lib/machines'
import { stackSummary, vdevDisk } from '@/lib/local-storage'
import { formatIec } from '@/lib/storage'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Stack, Database } from '@phosphor-icons/react'

function stateLine(state: string, error: string | undefined, absent: string, readAt?: string) {
  if (state === 'absent') return <p className="text-muted-foreground">{absent}</p>
  if (state === 'error') return <p className="text-warning">Could not be read: {error || 'no reason given'} — state unknown.</p>
  if (state === 'busy')
    return (
      <p className="text-warning">
        Previous read still running on the node (not restarted); showing the read of {readAt ?? 'an earlier cycle'}.
      </p>
    )
  return readAt ? <p className="text-muted-foreground">read {readAt}</p> : null
}

function diskLabel(machine: Machine, serial: string | undefined): string {
  if (!serial) return ''
  const bay = (machine.inventory?.drives ?? []).find((d) => d.serialNumber === serial)?.location
  return ` → ${serial}${bay ? ` (bay ${bay})` : ''}`
}

/** The node's ZFS pools: health, capacity, scrub, vdev tree joined to disks, datasets. */
export function ZfsCard({ machine }: { machine: Machine }) {
  const local = machine.storage?.local
  const observed = machine.storage?.observed ?? []
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Stack className="text-primary" /> ZFS on {machine.nodeRef || machine.name}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-xs font-mono">
        {!local ? (
          <p className="text-muted-foreground">{stackSummary(local)}.</p>
        ) : (
          <>
            {stateLine(local.zfs.state, local.zfs.error, 'No ZFS on this node (no zfs module loaded).', local.zfs.readAt)}
            {(local.zfs.pools ?? []).map((p) => (
              <div key={p.name} className="rounded-md border border-border p-2 space-y-1">
                <div className="flex flex-wrap gap-x-4">
                  <span className="font-bold">{p.name}</span>
                  <span className={p.health === 'ONLINE' ? 'text-accent' : 'text-destructive'}>{p.health || '?'}</span>
                  <span>
                    {formatIec(p.allocatedBytes ?? null)} / {formatIec(p.sizeBytes ?? null)} ({formatIec(p.freeBytes ?? null)} free
                    {p.capacityPercent !== undefined ? `, ${p.capacityPercent} %` : ''})
                  </span>
                  <span>frag {p.fragmentationPercent ?? '—'} %</span>
                </div>
                <div className={(p.scrubErrors ?? 0) > 0 ? 'text-destructive' : 'text-muted-foreground'}>scan: {p.scan || '—'}</div>
                <div className={p.errors && !/^No known data errors/i.test(p.errors) ? 'text-destructive' : 'text-muted-foreground'}>
                  errors: {p.errors || '—'}
                </div>
                <div className="flex gap-3 text-muted-foreground">
                  <span className="flex-1">vdev</span>
                  <span>state</span>
                  <span className="w-28 text-right">read/write/cksum</span>
                </div>
                {(p.vdevs ?? []).map((v, i) => {
                  const errs = (v.readErrors ?? 0) + (v.writeErrors ?? 0) + (v.checksumErrors ?? 0)
                  const bad = errs > 0 || (v.state && !['ONLINE', 'AVAIL', 'INUSE'].includes(v.state))
                  return (
                    <div key={`${i}-${v.name}`} className={`flex gap-3 ${bad ? 'text-warning' : ''}`} style={{ paddingLeft: `${v.depth * 1.25}rem` }}>
                      <span className="flex-1 truncate">
                        {v.name}
                        {diskLabel(machine, vdevDisk(v.name, observed, v.note)?.serialNumber)}
                        {v.note ? ` — ${v.note}` : ''}
                      </span>
                      <span>{v.state || ''}</span>
                      <span className="w-28 text-right">
                        {v.state ? `${v.readErrors ?? 0}/${v.writeErrors ?? 0}/${v.checksumErrors ?? 0}` : ''}
                      </span>
                    </div>
                  )
                })}
                {(p.vdevsOmitted ?? 0) > 0 && <div className="text-muted-foreground">… {p.vdevsOmitted} more row(s) not listed</div>}
              </div>
            ))}
            {(local.zfs.datasets ?? []).length > 0 && (
              <details>
                <summary className="cursor-pointer text-muted-foreground">
                  {(local.zfs.datasets ?? []).length} dataset(s)
                  {(local.zfs.datasetsOmitted ?? 0) > 0 ? ` (+${local.zfs.datasetsOmitted} not listed)` : ''}
                </summary>
                <table className="w-full mt-1">
                  <tbody>
                    {(local.zfs.datasets ?? []).map((d) => (
                      <tr key={d.name} className="border-t border-border">
                        <td className="pr-3">{d.name}</td>
                        <td className="pr-3">{d.type}</td>
                        <td className="pr-3 text-right">{formatIec(d.usedBytes ?? null)} used</td>
                        <td className="pr-3 text-right">{formatIec(d.availBytes ?? null)} avail</td>
                        <td>{d.mountPoint || '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </details>
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}

/** The node's mdadm arrays and LVM, with members/PVs named by disk serial. */
export function MdLvmCard({ machine }: { machine: Machine }) {
  const local = machine.storage?.local
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Database className="text-primary" /> md and LVM on {machine.nodeRef || machine.name}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-xs font-mono">
        {!local ? (
          <p className="text-muted-foreground">{stackSummary(local)}.</p>
        ) : (
          <>
            <div className="space-y-1">
              <div className="text-muted-foreground uppercase tracking-wide">mdadm</div>
              {stateLine(local.md.state, local.md.error, 'No md array on this node.', local.md.readAt)}
              {(local.md.arraysOmitted ?? 0) > 0 && (
                <p className="text-warning">{local.md.arraysOmitted} more array(s) not listed (status size limit) — their state is unknown.</p>
              )}
              {(local.md.arrays ?? []).map((a) => (
                <div key={a.name} className={a.degraded ? 'text-destructive' : ''}>
                  {a.name} {a.level ?? ''} {a.state}
                  {a.slots ? ` [${a.slots}]` : ''}
                  {a.raidDisks ? ` ${a.activeDisks}/${a.raidDisks} active` : ''}
                  {a.sync ? ` · ${a.sync}` : ''}
                  {a.degraded ? ' · DEGRADED' : ''}
                  <div className="pl-4 text-muted-foreground">
                    {(a.members ?? [])
                      .map((m) => `${m.device}${m.failed ? ' (failed)' : m.spare ? ' (spare)' : ''}${diskLabel(machine, m.serial)}`)
                      .join(' · ')}
                    {(a.membersOmitted ?? 0) > 0 ? ` · … ${a.membersOmitted} more member(s) not listed` : ''}
                  </div>
                </div>
              ))}
            </div>
            <div className="space-y-1">
              <div className="text-muted-foreground uppercase tracking-wide">LVM</div>
              {stateLine(local.lvm.state, local.lvm.error, 'LVM not in use on this node.', local.lvm.readAt)}
              {(local.lvm.vgs ?? []).map((vg) => (
                <div key={vg.name} className={(vg.attr ?? '')[3] === 'p' ? 'text-destructive' : ''}>
                  VG {vg.name}: {formatIec(vg.sizeBytes ?? null)} ({formatIec(vg.freeBytes ?? null)} free) · {vg.pvCount ?? 0} PV ·{' '}
                  {vg.lvCount ?? 0} LV · attr {vg.attr || '—'}
                  <div className="pl-4 text-muted-foreground">
                    PV{' '}
                    {(local.lvm.pvs ?? [])
                      .filter((pv) => pv.vg === vg.name)
                      .map((pv) => `${pv.name}${diskLabel(machine, pv.serial)}`)
                      .join(' · ') || '—'}
                  </div>
                  <div className="pl-4 text-muted-foreground">
                    LV{' '}
                    {(local.lvm.lvs ?? [])
                      .filter((lv) => lv.vg === vg.name)
                      .map((lv) => `${lv.name} ${formatIec(lv.sizeBytes ?? null)} ${lv.segType ?? ''}${lv.dataPercent ? ` ${lv.dataPercent} %` : ''}`)
                      .join(' · ') || '—'}
                  </div>
                </div>
              ))}
              {(local.lvm.omitted ?? 0) > 0 && <div className="text-muted-foreground">… {local.lvm.omitted} more not listed</div>}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
