import type { ZfsSourceResult, ZfsVdev } from '@/lib/zfs-source'
import { formatIec } from '@/lib/storage'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Stack } from '@phosphor-icons/react'

function VdevTree({ vdevs, depth = 0 }: { vdevs: ZfsVdev[]; depth?: number }) {
  return (
    <>
      {vdevs.map((v) => {
        const errs = v.read + v.write + v.cksum
        const bad = (v.state && v.state !== 'ONLINE') || errs > 0
        return (
          <div key={`${depth}-${v.name}`}>
            <div className={`flex gap-3 ${bad ? 'text-warning' : ''}`} style={{ paddingLeft: `${depth * 1.25}rem` }}>
              <span className="flex-1 truncate">{v.name}</span>
              <span>{v.state || '—'}</span>
              <span className="w-28 text-right">
                {v.read}/{v.write}/{v.cksum}
              </span>
            </div>
            {v.children.length > 0 && <VdevTree vdevs={v.children} depth={depth + 1} />}
          </div>
        )
      })}
    </>
  )
}

/** The hypervisor's ZFS pools, host disks and zvols — or why they cannot be shown. */
export function ZfsHostsCard({ result }: { result: ZfsSourceResult | null }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <Stack className="text-primary" /> Hypervisor ZFS
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 text-xs font-mono">
        {!result || result.status === 'unconfigured' ? (
          <p className="text-muted-foreground">
            Source not configured{result?.status === 'unconfigured' ? ` — ${result.reason}` : ''}. The pools under the VM
            nodes’ OSDs are not shown until a read-only Proxmox API reader is deployed (Settings → pveReader).
          </p>
        ) : result.status === 'error' ? (
          <p className="text-destructive">Proxmox reader failed: {result.message}</p>
        ) : (
          result.hosts.map((h) => (
            <div key={h.host} className="space-y-3">
              <div className="font-bold">{h.host}</div>
              {h.pools.map((p) => (
                <div key={p.name} className="rounded-md border border-border p-2 space-y-1">
                  <div className="flex flex-wrap gap-x-4">
                    <span className="font-bold">{p.name}</span>
                    <span className={p.health === 'ONLINE' ? 'text-accent' : 'text-destructive'}>{p.health}</span>
                    <span>
                      {formatIec(p.allocBytes)} / {formatIec(p.sizeBytes)} ({formatIec(p.freeBytes)} free)
                    </span>
                    <span>frag {p.fragPct ?? '—'} %</span>
                  </div>
                  <div className="text-muted-foreground">
                    scrub: {p.scrub.text || '—'} · {p.errors || '—'}
                  </div>
                  <div className="flex gap-3 text-muted-foreground">
                    <span className="flex-1">vdev</span>
                    <span>state</span>
                    <span className="w-28 text-right">read/write/cksum</span>
                  </div>
                  <VdevTree vdevs={p.vdevs} />
                </div>
              ))}
              {h.disks.length > 0 && (
                <table className="w-full">
                  <thead className="text-muted-foreground text-left">
                    <tr>
                      <th className="pr-3">disk</th>
                      <th className="pr-3">serial</th>
                      <th className="pr-3">model</th>
                      <th className="pr-3 text-right">size</th>
                      <th className="pr-3">used by</th>
                      <th className="pr-3">SMART</th>
                      <th>wearout</th>
                    </tr>
                  </thead>
                  <tbody>
                    {h.disks.map((d) => (
                      <tr key={d.devpath} className="border-t border-border">
                        <td className="pr-3">{d.devpath}</td>
                        <td className="pr-3">{d.serial || '—'}</td>
                        <td className="pr-3">{d.model || '—'}</td>
                        <td className="pr-3 text-right">{formatIec(d.sizeBytes)}</td>
                        <td className="pr-3">{d.used || '—'}</td>
                        <td className={`pr-3 ${/^(PASSED|OK)$/i.test(d.smartHealth) ? '' : 'text-warning'}`}>
                          {d.smartHealth}
                        </td>
                        <td>{d.wearout ?? 'n/a'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {h.datasets.length > 0 && (
                <div className="text-muted-foreground">
                  zvols:{' '}
                  {h.datasets
                    .map((d) => `${d.volume} ${formatIec(d.sizeBytes)}${d.usedBytes !== null ? ` (${formatIec(d.usedBytes)} used)` : ''}`)
                    .join(' · ')}
                </div>
              )}
            </div>
          ))
        )}
      </CardContent>
    </Card>
  )
}
