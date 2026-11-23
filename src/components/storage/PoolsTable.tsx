import type { ClassCapacity, PoolRow } from '@/lib/ceph-pools'
import type { Message } from '@/i18n'
import { pgLine } from '@/lib/storage-labels'
import { formatIec } from '@/lib/storage'
import { useI18n } from '@/i18n/context'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Cube } from '@phosphor-icons/react'

export function CapacityByClass({ classes }: { classes: ClassCapacity[] }) {
  const { t } = useI18n()
  if (classes.length === 0) return null
  return (
    <table className="w-full text-xs font-mono">
      <thead className="text-muted-foreground text-left">
        <tr>
          <th className="py-1 pr-3">{t('pool.col.class')}</th>
          <th className="pr-3">OSDs</th>
          <th className="pr-3">{t('pool.col.nodes')}</th>
          <th className="pr-3 text-right">{t('pool.col.usedRaw')}</th>
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
  const { t, tm } = useI18n()
  const text = (x: string | Message) => (typeof x === 'string' ? x : tm(x))
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <Cube className="text-primary" /> {t('pool.title', { count: pools.length })}
        </CardTitle>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        {pools.length === 0 ? (
          <p className="text-sm text-muted-foreground font-mono">{t('pool.empty')}</p>
        ) : (
          <table className="w-full text-xs font-mono">
            <thead className="text-muted-foreground text-left">
              <tr>
                <th className="py-1 pr-3">{t('pool.col.pool')}</th>
                <th className="pr-3">{t('pool.col.app')}</th>
                <th className="pr-3" title={t('pool.minSizeTitle')}>size/min</th>
                <th className="pr-3">{t('pool.col.crush')}</th>
                <th className="pr-3 text-right">PG</th>
                <th className="pr-3">{t('pool.col.pgStates')}</th>
                <th className="pr-3 text-right">{t('pool.col.stored')}</th>
                <th className="pr-3 text-right">{t('pool.col.pctUsed')}</th>
                <th className="text-right">{t('pool.col.maxAvail')}</th>
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
                  <td className="pr-3 text-muted-foreground">{p.crushRule ?? t('stv.notCollected')}</td>
                  <td className="pr-3 text-right">{p.pgs.total}</td>
                  <td
                    className={`pr-3 ${p.trouble === 'critical' ? 'text-destructive' : p.trouble === 'warning' ? 'text-warning' : ''}`}
                  >
                    {p.trouble ? text(p.troubleDetail) : text(pgLine(p.pgs))}
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
          {t('pool.footnote')}
        </p>
      </CardContent>
    </Card>
  )
}
