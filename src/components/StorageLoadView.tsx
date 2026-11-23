import { useMemo, useState } from 'react'
import { StorageEntry, createFrameClient } from '@/lib/frame-sdk'
import { placementOf } from '@/lib/ceph-placement'
import { formatIec } from '@/lib/storage'
import { useI18n } from '@/i18n/context'
import {
  FILL_CRITICAL_PCT,
  FILL_WARN_PCT,
  WRITE_SLOW_MS,
  buildVolumeRows,
  pathLine,
  slowSharedOsds,
  writeTone,
  type VolumeLoadRaw,
  type VolumeRow,
} from '@/lib/volume-load'
import { useStorageDiagnostic } from '@/components/storage/useStorageDiagnostic'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { TONE_TEXT } from '@/lib/thresholds'
import { ArrowClockwise, HardDrives, WarningCircle } from '@phosphor-icons/react'

const frame = createFrameClient()

const ms = (v: number | null | undefined) => (v === null || v === undefined ? '—' : `${Math.round(v)} ms`)

const FILL_TEXT: Record<NonNullable<VolumeRow['fill']>, string> = {
  ok: '',
  warning: 'text-warning',
  critical: 'text-destructive font-bold',
}

/**
 * Storage → Volumes: which workload writes where, physically. Read-only —
 * every judgement (pool → OSDs, the OSD on every write, fill levels) is in
 * volume-load.ts.
 */
export function StorageLoadView() {
  const { t } = useI18n()
  const { state: storageState } = useLiveResource<StorageEntry[]>(() => frame.storage.list(), [], [frame.storage.watchPath()])
  const entries = storageState.phase === 'ready' ? storageState.data : null
  const diag = useStorageDiagnostic(entries)
  const { state, reload } = useLiveResource<VolumeLoadRaw>(() => frame.cluster.volumeLoad(), [], [], 30_000)
  const [open, setOpen] = useState<string | null>(null)

  const raw = state.phase === 'ready' ? state.data : null
  // Until the Ceph diagnostic answers, the OSD list is unknown, not empty:
  // the rows say so and the banner says it could not be evaluated.
  const osdsKnown = diag.promAvailable
  const rows = useMemo(() => {
    if (!raw) return []
    const mgrPools = new Map<string, { crushRule: string | null; size: number | null }>()
    for (const p of placementOf(entries ?? [])?.pools ?? []) {
      mgrPools.set(p.name, { crushRule: p.crushRule || null, size: p.size ?? null })
    }
    return buildVolumeRows({ ...raw, osds: diag.osds, osdsKnown, mgrPools })
  }, [raw, diag.osds, osdsKnown, entries])
  const shared = useMemo(() => slowSharedOsds(rows), [rows])
  const fillAlerts = rows.filter((r) => r.fill === 'warning' || r.fill === 'critical')
  const statsMissing = raw !== null && rows.length > 0 && rows.every((r) => r.usePct === null)

  if (!raw) return <LiveStates state={state} emptyLabel={t('vol.none')} />

  return (
    <div className="space-y-6">
      {raw.unreadable.length > 0 && (
        <Card className="border-warning">
          <CardContent className="pt-6 text-xs font-mono text-warning">
            {t('vol.unreadable', { list: raw.unreadable.join(', ') })}
          </CardContent>
        </Card>
      )}

      {(shared.length > 0 || fillAlerts.length > 0) && (
        <Card className={shared.length > 0 || fillAlerts.some((r) => r.fill === 'critical') ? 'border-destructive' : undefined}>
          <CardHeader>
            <CardTitle className="font-mono text-base flex items-center gap-2">
              <WarningCircle className="text-warning" /> {t('sa.title')}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-1 text-xs font-mono">
              {shared.map((s) => (
                <li key={`osd-${s.osd}`} className="text-destructive">
                  {t('vol.shared', {
                    osd: s.osd,
                    host: s.host,
                    poolWord: { key: 'vol.pool', params: { count: s.pools.length } },
                    pools: s.pools.join(', '),
                    ms: ms(s.commitMs),
                    volumes: { key: 'vol.volumesWait', params: { count: s.volumes } },
                  })}
                </li>
              ))}
              {fillAlerts.map((r) => (
                <li key={`fill-${r.key}`} className={r.fill === 'critical' ? 'text-destructive' : 'text-warning'}>
                  {t('vol.fillAlert', { key: r.key, pct: r.usePct!.toFixed(1), used: formatIec(r.usedBytes), size: formatIec(r.sizeBytes) })}
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <HardDrives className="text-primary" /> {t('vol.title', { count: rows.length })}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={() => {
                reload()
                diag.reload()
              }}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {rows.length === 0 ? (
            <p className="text-sm text-muted-foreground font-mono">{t('vol.empty')}</p>
          ) : (
            <table className="w-full text-xs font-mono">
              <thead className="text-muted-foreground text-left">
                <tr>
                  <th className="py-1 pr-3">{t('vol.col.claim')}</th>
                  <th className="pr-3">{t('vol.col.usedBy')}</th>
                  <th className="pr-3">{t('pool.col.nodes')}</th>
                  <th className="pr-3 text-right">{t('vol.col.usedSize')}</th>
                  <th className="pr-3">{t('pool.col.class')}</th>
                  <th className="pr-3">{t('vol.col.path')}</th>
                  <th className="pr-3 text-right" title={t('vol.writeTitle')}>
                    {t('vol.col.write')}
                  </th>
                  <th className="text-right" title={t('vol.worstTitle')}>
                    {t('vol.col.worst')}
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <VolumeLine key={r.key} row={r} open={open === r.key} onToggle={() => setOpen(open === r.key ? null : r.key)} />
                ))}
              </tbody>
            </table>
          )}
          {!osdsKnown && (
            <p className="mt-2 text-xs text-warning font-mono">
              {t('vol.noOsds', { why: diag.error ? ` (${diag.error})` : diag.loading ? t('vol.whyYet') : t('vol.whyProm') })}
            </p>
          )}
          <p className="mt-2 text-[10px] text-muted-foreground font-mono">
            {t('vol.footnote', {
              src: statsMissing ? t('vol.statsMissing') : 'kubelet',
              warn: FILL_WARN_PCT,
              crit: FILL_CRITICAL_PCT,
              slow: WRITE_SLOW_MS,
            })}
          </p>
        </CardContent>
      </Card>
    </div>
  )
}

function VolumeLine({ row: r, open, onToggle }: { row: VolumeRow; open: boolean; onToggle: () => void }) {
  const { t, tm } = useI18n()
  const users = r.consumers.map((c) => c.workload)
  return (
    <>
      <tr className="border-t border-border hover:bg-muted/40">
        <td className="py-1 pr-3">
          <button type="button" className="text-left hover:underline" aria-expanded={open} onClick={onToggle}>
            <span className="text-muted-foreground">{r.namespace}/</span>
            <span className="font-bold">{r.name}</span>
          </button>
          {r.phase !== 'Bound' && <span className="ml-1 text-warning">{r.phase}</span>}
        </td>
        <td className="pr-3">{users.length ? users.join(', ') : <span className="text-muted-foreground">{t('vol.unused')}</span>}</td>
        <td className="pr-3">{r.nodes.join(', ') || '—'}</td>
        <td className={`pr-3 text-right ${r.fill ? FILL_TEXT[r.fill] : ''}`}>
          {r.usePct === null ? (
            <span className="text-muted-foreground">? / {formatIec(r.sizeBytes)}</span>
          ) : (
            <>
              {formatIec(r.usedBytes)} / {formatIec(r.sizeBytes)} ({r.usePct.toFixed(1)} %)
            </>
          )}
        </td>
        <td className="pr-3">{r.storageClass ?? '—'}</td>
        <td className="pr-3 text-muted-foreground">{tm(pathLine(r.path))}</td>
        <td className={`pr-3 text-right ${TONE_TEXT[writeTone(r.likelyWrite?.ms ?? null)]}`}>
          {r.likelyWrite ? `${ms(r.likelyWrite.ms)} (osd.${r.likelyWrite.osd})` : '—'}
        </td>
        <td className={`text-right ${TONE_TEXT[writeTone(r.worst24h?.ms ?? null)]}`}>
          {r.worst24h ? `${ms(r.worst24h.ms)} (osd.${r.worst24h.osd})` : '—'}
        </td>
      </tr>
      {open && (
        <tr className="bg-muted/20">
          <td colSpan={8} className="px-3 py-2">
            <VolumeDetail row={r} />
          </td>
        </tr>
      )}
    </>
  )
}

function VolumeDetail({ row: r }: { row: VolumeRow }) {
  const { t } = useI18n()
  return (
    <div className="space-y-2">
      <div>
        {t('vol.reclaim', { policy: r.reclaimPolicy ?? '—' })}
        {r.consumers.map((c) => (
          <div key={c.workload}>
            {c.workload}: {c.pods.map((p) => t('vol.podOn', { name: p.name, node: p.node || t('vol.unscheduled'), phase: p.phase })).join(', ')}
          </div>
        ))}
      </div>
      {r.path.kind === 'ceph' && (
        <table className="text-xs font-mono">
          <thead className="text-muted-foreground text-left">
            <tr>
              <th className="pr-3">OSD</th>
              <th className="pr-3">{t('vol.col.host')}</th>
              <th className="pr-3">{t('pool.col.class')}</th>
              <th className="pr-3">{t('osd.col.disk')}</th>
              <th className="pr-3 text-right">commit</th>
              <th className="pr-3 text-right">{t('vol.col.worst')}</th>
              <th>{t('vol.col.everyWrite')}</th>
            </tr>
          </thead>
          <tbody>
            {r.path.osds.map((o) => (
              <tr key={o.id}>
                <td className="pr-3">osd.{o.id}</td>
                <td className="pr-3">{o.host}</td>
                <td className="pr-3">{o.deviceClass}</td>
                <td className="pr-3">{o.disk}</td>
                <td className={`pr-3 text-right ${TONE_TEXT[writeTone(o.commitMs)]}`}>{ms(o.commitMs)}</td>
                <td className={`pr-3 text-right ${TONE_TEXT[writeTone(o.worst24hMs)]}`}>{ms(o.worst24hMs)}</td>
                <td>{o.onEveryWrite ? t('common.yes') : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
