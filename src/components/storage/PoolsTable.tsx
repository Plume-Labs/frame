import type { ClassCapacity, PoolRow } from '@/lib/ceph-pools'
import { pgLine } from '@/lib/storage-labels'
import { formatIec, NOT_COLLECTED } from '@/lib/storage'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Cube } from '@phosphor-icons/react'

export function CapacityByClass({ classes }: { classes: ClassCapacity[] }) {
  if (classes.length === 0) return null
  return (
    <table className="w-full text-xs font-mono">
      <thead className="text-muted-foreground text-left">
        <tr>
          <th className="py-1 pr-3">class</th>
          <th className="pr-3">OSDs</th>
          <th className="pr-3">nodes</th>
          <th className="pr-3 text-right">used / raw</th>
        </tr>
      </thead>
      <tbody>
        {classes.map((c) => (
          <tr key={c.deviceClass} className="border-t border-border">
            <td className="py-1 pr-3 font-bold">{c.deviceClass}</td>
            <td className="pr-3">{c.osds.map((i) => `osd.${i}`).join(', ') || '—'}</td>
            <td className="pr-3">{c.hosts.join(', ') || '—'}</td>
            <td className="pr-3 text-right">
              {formatIec(c.usedBytes)} / {formatIec(c.totalBytes)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export function PoolsTable({ pools }: { pools: PoolRow[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <Cube className="text-primary" /> Pools ({pools.length})
        </CardTitle>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        {pools.length === 0 ? (
          <p className="text-sm text-muted-foreground font-mono">No pool reported by the ceph-mgr exporter.</p>
        ) : (
          <table className="w-full text-xs font-mono">
            <thead className="text-muted-foreground text-left">
              <tr>
                <th className="py-1 pr-3">pool</th>
                <th className="pr-3">app</th>
                <th className="pr-3" title={`min_size: ${NOT_COLLECTED}`}>size/min</th>
                <th className="pr-3">CRUSH rule</th>
                <th className="pr-3 text-right">PG</th>
                <th className="pr-3">PG states</th>
                <th className="pr-3 text-right">stored</th>
                <th className="pr-3 text-right">%used</th>
                <th className="text-right">max avail</th>
              </tr>
            </thead>
            <tbody>
              {pools.map((p) => (
                <tr key={p.id} className="border-t border-border">
                  <td className="py-1 pr-3 font-bold">{p.name}</td>
                  <td className="pr-3">{p.application || '—'}</td>
                  <td className="pr-3">
                    {p.size !== null ? p.size : p.erasure ? `EC ${p.erasure}` : '—'}/{p.minSize ?? '?'}
                  </td>
                  <td className="pr-3 text-muted-foreground">{p.crushRule ?? NOT_COLLECTED}</td>
                  <td className="pr-3 text-right">{p.pgs.total}</td>
                  <td
                    className={`pr-3 ${p.trouble === 'critical' ? 'text-destructive' : p.trouble === 'warning' ? 'text-warning' : ''}`}
                  >
                    {p.trouble ? p.troubleDetail : pgLine(p.pgs)}
                  </td>
                  <td className="pr-3 text-right">{formatIec(p.storedBytes)}</td>
                  <td className="pr-3 text-right">{p.percentUsed === null ? '—' : `${p.percentUsed.toFixed(2)} %`}</td>
                  <td className="text-right">{formatIec(p.maxAvailBytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="mt-2 text-[10px] text-muted-foreground font-mono">
          min_size and CRUSH rule: {NOT_COLLECTED} (not exported by the ceph-mgr; needs its REST API). Max avail is
          what the pool can still take after replication.
        </p>
      </CardContent>
    </Card>
  )
}
