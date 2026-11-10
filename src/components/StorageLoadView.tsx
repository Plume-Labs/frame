import { useMemo, useState } from 'react'
import { StorageEntry, createFrameClient } from '@/lib/frame-sdk'
import { placementOf } from '@/lib/ceph-placement'
import { formatIec, NOT_COLLECTED } from '@/lib/storage'
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

  if (!raw) return <LiveStates state={state} emptyLabel="No volume" />

  return (
    <div className="space-y-6">
      {(shared.length > 0 || fillAlerts.length > 0) && (
        <Card className={shared.length > 0 || fillAlerts.some((r) => r.fill === 'critical') ? 'border-destructive' : undefined}>
          <CardHeader>
            <CardTitle className="font-mono text-base flex items-center gap-2">
              <WarningCircle className="text-warning" /> To address
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="space-y-1 text-xs font-mono">
              {shared.map((s) => (
                <li key={`osd-${s.osd}`} className="text-destructive">
                  osd.{s.osd} on {s.host} takes part in every write of pool{s.pools.length > 1 ? 's' : ''}{' '}
                  {s.pools.join(', ')} (the only OSD of its host) and commits in {ms(s.commitMs)}: {s.volumes} volume
                  {s.volumes > 1 ? 's' : ''} wait for it on each write.
                </li>
              ))}
              {fillAlerts.map((r) => (
                <li key={`fill-${r.key}`} className={r.fill === 'critical' ? 'text-destructive' : 'text-warning'}>
                  {r.key} is {r.usePct!.toFixed(1)} % full ({formatIec(r.usedBytes)} of {formatIec(r.sizeBytes)}).
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <HardDrives className="text-primary" /> Volumes ({rows.length})
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
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {rows.length === 0 ? (
            <p className="text-sm text-muted-foreground font-mono">No PersistentVolumeClaim in the cluster.</p>
          ) : (
            <table className="w-full text-xs font-mono">
              <thead className="text-muted-foreground text-left">
                <tr>
                  <th className="py-1 pr-3">claim</th>
                  <th className="pr-3">used by</th>
                  <th className="pr-3">nodes</th>
                  <th className="pr-3 text-right">used / size</th>
                  <th className="pr-3">class</th>
                  <th className="pr-3">physical path</th>
                  <th className="pr-3 text-right" title="Commit latency of the slowest OSD that may hold a copy, right now">
                    write
                  </th>
                  <th className="text-right" title="Worst 5-minute commit latency of any OSD on the path, last 24 h">
                    worst 24 h
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
              OSD figures not collected{diag.error ? ` (${diag.error})` : diag.loading ? ' yet' : ' (Prometheus unreachable)'}: the
              write path, its latency and the slow-OSD banner cannot be evaluated.
            </p>
          )}
          <p className="mt-2 text-[10px] text-muted-foreground font-mono">
            Fill from kubelet_volume_stats ({statsMissing ? `${NOT_COLLECTED} on this cluster` : 'kubelet'}); flagged at{' '}
            {FILL_WARN_PCT} % and {FILL_CRITICAL_PCT} %. Write latency is the slowest commit of the OSDs the pool places
            copies on (ceph_osd_commit_latency_ms); above {WRITE_SLOW_MS} ms it is flagged. Local volumes have no
            per-disk figure here. Read-only: nothing on this screen changes a volume.
          </p>
        </CardContent>
      </Card>
    </div>
  )
}

function VolumeLine({ row: r, open, onToggle }: { row: VolumeRow; open: boolean; onToggle: () => void }) {
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
        <td className="pr-3">{users.length ? users.join(', ') : <span className="text-muted-foreground">unused</span>}</td>
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
        <td className="pr-3 text-muted-foreground">{pathLine(r.path)}</td>
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
  return (
    <div className="space-y-2">
      <div>
        reclaim policy: {r.reclaimPolicy ?? '—'}
        {r.consumers.map((c) => (
          <div key={c.workload}>
            {c.workload}: {c.pods.map((p) => `${p.name} on ${p.node || 'unscheduled'} (${p.phase})`).join(', ')}
          </div>
        ))}
      </div>
      {r.path.kind === 'ceph' && (
        <table className="text-xs font-mono">
          <thead className="text-muted-foreground text-left">
            <tr>
              <th className="pr-3">OSD</th>
              <th className="pr-3">host</th>
              <th className="pr-3">class</th>
              <th className="pr-3">disk</th>
              <th className="pr-3 text-right">commit</th>
              <th className="pr-3 text-right">worst 24 h</th>
              <th>on every write</th>
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
                <td>{o.onEveryWrite ? 'yes' : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
