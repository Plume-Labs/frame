import { useState } from 'react'
import { toast } from 'sonner'
import { BackupStatus, Resilience, createFrameClient, coreListPath, crdListPath } from '@/lib/frame-sdk'
import { config } from '@/lib/frame-config'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Stat } from '@/components/Primitives'
import { ShieldWarning, ArrowClockwise, Database, ShieldCheck, Archive, Play } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'

const frame = createFrameClient()

export function ResilienceView() {
  const { t, tn, locale } = useI18n()
  const cephNs = config().namespaces.ceph
  const veleroNs = config().namespaces.velero
  // resilience() reads PodDisruptionBudgets, Pods (for restart hotspots), and
  // — via ceph() — the CephCluster/CephBlockPool CRs. All real objects; the
  // cluster-wide Pod watch already covers the osd/mon pods ceph() also reads.
  const { state, reload } = useLiveResource<Resilience>(
    () => frame.cluster.resilience(),
    [],
    [
      crdListPath('policy', 'v1', 'poddisruptionbudgets'),
      coreListPath('pods'),
      crdListPath('ceph.rook.io', 'v1', 'cephclusters', cephNs),
      crdListPath('ceph.rook.io', 'v1', 'cephblockpools', cephNs),
    ],
  )
  const r = state.phase === 'ready' ? state.data : null
  const { state: bkState, reload: reloadBk } = useLiveResource<BackupStatus | null>(
    () => frame.cluster.backups(),
    [],
    [
      crdListPath('velero.io', 'v1', 'backups', veleroNs),
      crdListPath('velero.io', 'v1', 'backupstoragelocations', veleroNs),
      crdListPath('velero.io', 'v1', 'schedules', veleroNs),
    ],
  )
  const bk = bkState.phase === 'ready' ? bkState.data : null
  const [triggering, setTriggering] = useState(false)

  async function doBackup() {
    setTriggering(true)
    try {
      const { name } = await frame.cluster.triggerBackup()
      toast.success(t('res.backup.started', { name }))
      reloadBk()
    } catch (e) {
      toast.error(t('res.backup.failed'), { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setTriggering(false)
    }
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ShieldWarning className="text-primary" />
            {t('res.title')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            {t('res.desc')}
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel={t('res.empty')} />

      {r && (
        <>
          <Card>
            <CardHeader>
              <CardTitle className="font-mono text-lg flex items-center gap-2">
                <Database className="text-primary" />
                {t('res.durability')}
              </CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4">
              <Stat label={t('res.cephHealth')} value={r.cephHealth.replace('HEALTH_', '')} tone={r.cephHealth === 'HEALTH_OK' ? 'accent' : 'warning'} />
              <Stat label={t('res.osdsUp')} value={`${r.cephOsds}`} />
              <Stat label={t('res.replication')} value={r.cephReplication ? `×${r.cephReplication}` : '—'} />
              <Stat label={t('res.totalRestarts')} value={`${r.totalRestarts}`} tone={r.totalRestarts ? 'warning' : 'accent'} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="font-mono text-lg flex items-center gap-2">
                <ShieldCheck className="text-primary" />
                {t('res.pdb')}
                {r.pdbAtRisk > 0 && (
                  <Badge variant="outline" className="ml-auto font-mono text-warning border-warning/40">
                    {t('res.pdb.atRisk', { n: r.pdbAtRisk })}
                  </Badge>
                )}
              </CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {r.pdbs.map((p) => (
                <div key={`${p.namespace}/${p.name}`} className="p-3 rounded-lg border border-border bg-secondary/30 space-y-1">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-xs font-bold truncate">{p.name}</span>
                    <Badge variant="outline" className={`text-[10px] font-mono ${p.disruptionsAllowed > 0 ? 'text-accent' : 'text-warning'}`}>
                      {t('res.pdb.allowed', { n: p.disruptionsAllowed })}
                    </Badge>
                  </div>
                  <div className="text-[10px] text-muted-foreground font-mono">
                    {t('res.pdb.healthy', { ns: p.namespace, cur: p.currentHealthy, des: p.desiredHealthy })}
                  </div>
                </div>
              ))}
            </CardContent>
          </Card>

          {r.hotspots.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle className="font-mono text-lg">{t('res.hotspots')}</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1">
                {r.hotspots.map((h) => (
                  <div key={`${h.namespace}/${h.pod}`} className="flex items-center justify-between text-xs font-mono">
                    <span className="text-muted-foreground truncate">{h.namespace}/{h.pod}</span>
                    <Badge variant="outline" className="text-[10px] text-warning border-warning/40">
                      {tn('res.restarts', h.restarts)}
                    </Badge>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </>
      )}

      {/* ── Backups / DR (Velero → Ceph RGW) ── */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-base flex items-center gap-2">
            <Archive size={16} className="text-primary" /> {t('res.backups')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={doBackup}
              disabled={triggering}
            >
              <Play className={triggering ? 'animate-spin' : ''} size={12} />
              {triggering ? t('res.backup.starting') : t('res.backup.now')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <LiveStates state={bkState} emptyLabel={t('res.backup.empty')} />
          {bk && (
            <>
              <div className="flex flex-wrap items-center gap-2 text-[11px] font-mono">
                <Badge variant="outline" className={`border-current ${bk.storageReady ? 'text-accent' : 'text-destructive'}`}>
                  {bk.storageReady ? t('res.storage.up') : t('res.storage.down')}
                </Badge>
                <Badge variant="outline" className="border-current text-foreground">
                  {bk.schedule ? t('res.schedule', { schedule: bk.schedule }) : t('res.noSchedule')}
                </Badge>
                {bk.lastSuccess && (
                  <span className="text-muted-foreground">{t('res.lastSuccess', { when: new Date(bk.lastSuccess).toLocaleString(locale) })}</span>
                )}
              </div>
              {bk.recent.map((run) => (
                <div key={run.name} className="flex flex-wrap items-center gap-2 text-[11px] font-mono">
                  <span className="flex-1 truncate">{run.name}</span>
                  <span className="text-muted-foreground truncate max-w-[10rem]">{run.namespaces.join(',')}</span>
                  <Badge variant="secondary" className="text-[10px]">{tn('res.items', run.items)}</Badge>
                  {run.errors > 0 && <Badge variant="outline" className="border-current text-destructive text-[10px]">{t('res.errors', { n: run.errors })}</Badge>}
                  <Badge variant="outline" className={`border-current text-[10px] ${run.phase === 'Completed' ? 'text-accent' : run.phase === 'InProgress' ? 'text-warning' : 'text-destructive'}`}>{run.phase}</Badge>
                </div>
              ))}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

