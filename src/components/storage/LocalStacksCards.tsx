import type { Machine } from '@/lib/machines'
import { stackSummary, vdevDisk } from '@/lib/local-storage'
import { formatIec } from '@/lib/storage'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Stack, Database } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'
import type { MessageKey } from '@/i18n'

type T = ReturnType<typeof useI18n>['t']

function stateLine(t: T, state: string, error: string | undefined, absent: MessageKey, readAt?: string) {
  if (state === 'absent') return <p className="text-muted-foreground">{t(absent)}</p>
  if (state === 'error') return <p className="text-warning">{t('lsc.unreadable', { error: error || t('lst.noReason') })}</p>
  if (state === 'busy')
    return <p className="text-warning">{t('lsc.busy', { at: readAt ?? t('lsc.earlierCycle') })}</p>
  return readAt ? <p className="text-muted-foreground">{t('lsc.readAt', { at: readAt })}</p> : null
}

function diskLabel(t: T, machine: Machine, serial: string | undefined): string {
  if (!serial) return ''
  const bay = (machine.inventory?.drives ?? []).find((d) => d.serialNumber === serial)?.location
  return ` → ${serial}${bay ? ` (${t('lsc.bay', { bay })})` : ''}`
}

/** The node's ZFS pools: health, capacity, scrub, vdev tree joined to disks, datasets. */
export function ZfsCard({ machine }: { machine: Machine }) {
  const { t, tm, tn } = useI18n()
  const local = machine.storage?.local
  const observed = machine.storage?.observed ?? []
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Stack className="text-primary" /> {t('lsc.zfsOn', { node: machine.nodeRef || machine.name })}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-xs font-mono">
        {!local ? (
          <p className="text-muted-foreground">{tm(stackSummary(local))}.</p>
        ) : (
          <>
            {stateLine(t, local.zfs.state, local.zfs.error, 'lsc.noZfs', local.zfs.readAt)}
            {(local.zfs.pools ?? []).map((p) => (
              <div key={p.name} className="rounded-md border border-border p-2 space-y-1">
                <div className="flex flex-wrap gap-x-4">
                  <span className="font-bold">{p.name}</span>
                  <span className={p.health === 'ONLINE' ? 'text-accent' : 'text-destructive'}>{p.health || '?'}</span>
                  <span>
                    {formatIec(p.allocatedBytes ?? null)} / {formatIec(p.sizeBytes ?? null)} ({t('lsc.free', { size: formatIec(p.freeBytes ?? null) })}
                    {p.capacityPercent !== undefined ? `, ${p.capacityPercent} %` : ''})
                  </span>
                  <span>{t('lsc.frag', { pct: p.fragmentationPercent ?? '—' })}</span>
                </div>
                <div className={(p.scrubErrors ?? 0) > 0 ? 'text-destructive' : 'text-muted-foreground'}>{t('lsc.scan')} {p.scan || '—'}</div>
                <div className={p.errors && !/^No known data errors/i.test(p.errors) ? 'text-destructive' : 'text-muted-foreground'}>
                  {t('lsc.errors')} {p.errors || '—'}
                </div>
                <div className="flex gap-3 text-muted-foreground">
                  <span className="flex-1">vdev</span>
                  <span>{t('osd.col.state')}</span>
                  <span className="w-28 text-right">read/write/cksum</span>
                </div>
                {(p.vdevs ?? []).map((v, i) => {
                  const errs = (v.readErrors ?? 0) + (v.writeErrors ?? 0) + (v.checksumErrors ?? 0)
                  const bad = errs > 0 || (v.state && !['ONLINE', 'AVAIL', 'INUSE'].includes(v.state))
                  return (
                    <div key={`${i}-${v.name}`} className={`flex gap-3 ${bad ? 'text-warning' : ''}`} style={{ paddingLeft: `${v.depth * 1.25}rem` }}>
                      <span className="flex-1 truncate">
                        {v.name}
                        {diskLabel(t, machine, vdevDisk(v.name, observed, v.note)?.serialNumber)}
                        {v.note ? ` — ${v.note}` : ''}
                      </span>
                      <span>{v.state || ''}</span>
                      <span className="w-28 text-right">
                        {v.state ? `${v.readErrors ?? 0}/${v.writeErrors ?? 0}/${v.checksumErrors ?? 0}` : ''}
                      </span>
                    </div>
                  )
                })}
                {(p.vdevsOmitted ?? 0) > 0 && <div className="text-muted-foreground">{tn('lsc.rowsOmitted', p.vdevsOmitted ?? 0)}</div>}
              </div>
            ))}
            {(local.zfs.datasets ?? []).length > 0 && (
              <details>
                <summary className="cursor-pointer text-muted-foreground">
                  {tn('lsc.datasets', (local.zfs.datasets ?? []).length)}
                  {(local.zfs.datasetsOmitted ?? 0) > 0 ? ` (${tn('lsc.datasetsOmitted', local.zfs.datasetsOmitted ?? 0)})` : ''}
                </summary>
                <table className="w-full mt-1">
                  <tbody>
                    {(local.zfs.datasets ?? []).map((d) => (
                      <tr key={d.name} className="border-t border-border">
                        <td className="pr-3">{d.name}</td>
                        <td className="pr-3">{d.type}</td>
                        <td className="pr-3 text-right">{t('lsc.used', { size: formatIec(d.usedBytes ?? null) })}</td>
                        <td className="pr-3 text-right">{t('lsc.avail', { size: formatIec(d.availBytes ?? null) })}</td>
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
  const { t, tm, tn } = useI18n()
  const local = machine.storage?.local
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-base flex items-center gap-2">
          <Database className="text-primary" /> {t('lsc.mdLvmOn', { node: machine.nodeRef || machine.name })}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-xs font-mono">
        {!local ? (
          <p className="text-muted-foreground">{tm(stackSummary(local))}.</p>
        ) : (
          <>
            <div className="space-y-1">
              <div className="text-muted-foreground uppercase tracking-wide">mdadm</div>
              {stateLine(t, local.md.state, local.md.error, 'lsc.noMd', local.md.readAt)}
              {(local.md.arraysOmitted ?? 0) > 0 && (
                <p className="text-warning">{tn('lsc.arraysOmitted', local.md.arraysOmitted ?? 0)}</p>
              )}
              {(local.md.arrays ?? []).map((a) => (
                <div key={a.name} className={a.degraded ? 'text-destructive' : ''}>
                  {a.name} {a.level ?? ''} {a.state}
                  {a.slots ? ` [${a.slots}]` : ''}
                  {a.raidDisks ? ` ${t('lsc.active', { active: a.activeDisks ?? 0, total: a.raidDisks })}` : ''}
                  {a.sync ? ` · ${a.sync}` : ''}
                  {a.degraded ? ` · ${t('lsc.degraded')}` : ''}
                  <div className="pl-4 text-muted-foreground">
                    {(a.members ?? [])
                      .map((m) => `${m.device}${m.failed ? ` (${t('lsc.failed')})` : m.spare ? ` (${t('lsc.spare')})` : ''}${diskLabel(t, machine, m.serial)}`)
                      .join(' · ')}
                    {(a.membersOmitted ?? 0) > 0 ? ` · ${tn('lsc.membersOmitted', a.membersOmitted ?? 0)}` : ''}
                  </div>
                </div>
              ))}
            </div>
            <div className="space-y-1">
              <div className="text-muted-foreground uppercase tracking-wide">LVM</div>
              {stateLine(t, local.lvm.state, local.lvm.error, 'lsc.noLvm', local.lvm.readAt)}
              {(local.lvm.vgs ?? []).map((vg) => (
                <div key={vg.name} className={(vg.attr ?? '')[3] === 'p' ? 'text-destructive' : ''}>
                  {t('lsc.vg', {
                    name: vg.name,
                    size: formatIec(vg.sizeBytes ?? null),
                    free: formatIec(vg.freeBytes ?? null),
                    pv: vg.pvCount ?? 0,
                    lv: vg.lvCount ?? 0,
                    attr: vg.attr || '—',
                  })}
                  <div className="pl-4 text-muted-foreground">
                    PV{' '}
                    {(local.lvm.pvs ?? [])
                      .filter((pv) => pv.vg === vg.name)
                      .map((pv) => `${pv.name}${diskLabel(t, machine, pv.serial)}`)
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
              {(local.lvm.omitted ?? 0) > 0 && <div className="text-muted-foreground">{tn('lsc.lvmOmitted', local.lvm.omitted ?? 0)}</div>}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
