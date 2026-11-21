import { useState } from 'react'
import { toast } from 'sonner'
import { ClusterNodeInfo, coreListPath, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Stat, Meter } from '@/components/Primitives'
import { Cpu, ArrowClockwise, CheckCircle, XCircle, LockSimple, LockSimpleOpen, Eject } from '@phosphor-icons/react'
import { useI18n } from '@/i18n/context'

const frame = createFrameClient()

export function ClusterNodesView() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<ClusterNodeInfo[]>(
    () => frame.cluster.nodes(),
    [],
    // The Node objects driving readiness/roles/capacity are watched, so a
    // node joining, leaving, or changing Ready refreshes immediately.
    [coreListPath('nodes')],
    // The cpu/mem usage columns come from metrics-server, which has no watch
    // support of its own and drifts continuously rather than on a discrete
    // event — the watch above would leave them frozen between Node changes,
    // which can be a long time on a quiet cluster. Slow poll because the
    // watch already covers everything else; this only needs to catch the
    // one thing that moves on its own.
    30_000,
  )
  const [cordonTarget, setCordonTarget] = useState<ClusterNodeInfo | null>(null)
  const [drainTarget, setDrainTarget] = useState<ClusterNodeInfo | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  async function setCordon(node: ClusterNodeInfo, unschedulable: boolean) {
    setBusy(node.name)
    try {
      await frame.cluster.cordon(node.name, unschedulable)
      toast.success(t(unschedulable ? 'cn.cordoned' : 'cn.uncordoned', { name: node.name }))
      reload()
    } catch (e) {
      toast.error(t(unschedulable ? 'cn.cordonFailed' : 'cn.uncordonFailed', { name: node.name }), {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusy(null)
      setCordonTarget(null)
    }
  }

  async function drain(node: ClusterNodeInfo) {
    setBusy(node.name)
    try {
      const { evicted, skipped, blocked } = await frame.cluster.drain(node.name)
      const detail =
        t('cn.drainDetail', { evicted, skipped }) + (blocked ? ` · ${t('cn.drainBlocked', { blocked })}` : '')
      // A blocked pod is not a failure of the drain, but it does mean the node
      // is not actually empty — say so rather than showing a plain success.
      if (blocked) toast.warning(t('cn.partiallyDrained', { name: node.name }), { description: detail })
      else toast.success(t('cn.drained', { name: node.name }), { description: detail })
      reload()
    } catch (e) {
      toast.error(t('cn.drainFailed', { name: node.name }), {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusy(null)
      setDrainTarget(null)
    }
  }

  const nodes = state.phase === 'ready' ? state.data : []
  const ready = nodes.filter((n) => n.ready).length
  const totalCpu = nodes.reduce((s, n) => s + n.cpuCores, 0)
  const usedCpu = nodes.reduce((s, n) => s + (n.cpuUsedCores ?? 0), 0)
  const totalMem = nodes.reduce((s, n) => s + n.memGiB, 0)
  const usedMem = nodes.reduce((s, n) => s + (n.memUsedGiB ?? 0), 0)

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Cpu className="text-primary" />
            {t('cn.title')}
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
        {state.phase === 'ready' && (
          <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <Stat label={t('cn.nodesReady')} value={`${ready} / ${nodes.length}`} tone="accent" />
            <Stat
              label={t('cn.cpuInUse')}
              value={t('cn.coresOf', { used: usedCpu.toFixed(1), total: totalCpu.toFixed(0) })}
            />
            <Stat
              label={t('cn.memInUse')}
              value={`${usedMem.toFixed(1)} / ${totalMem.toFixed(0)} GiB`}
            />
            <Stat label={t('cn.kubelet')} value={nodes[0]?.kubeletVersion ?? '—'} size="sm" />
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel={t('cn.empty')} />

      {state.phase === 'ready' &&
        nodes.map((n) => {
          const cpuPct = n.cpuCores ? ((n.cpuUsedCores ?? 0) / n.cpuCores) * 100 : 0
          const memPct = n.memGiB ? ((n.memUsedGiB ?? 0) / n.memGiB) * 100 : 0
          return (
            <Card key={n.name}>
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div className="flex items-center gap-3 min-w-0">
                    <Cpu className="text-primary shrink-0" size={20} weight="duotone" />
                    <div className="min-w-0">
                      <CardTitle className="font-mono text-lg truncate">{n.name}</CardTitle>
                      <p className="text-xs text-muted-foreground font-mono">
                        {n.roles.join(', ')} · {n.kubeletVersion} · {n.os}
                      </p>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    {n.unschedulable && (
                      <Badge variant="outline" className="font-mono text-warning border-current">
                        {t('cn.cordonedBadge')}
                      </Badge>
                    )}
                    <Badge
                      variant="outline"
                      className={`font-mono ${n.ready ? 'text-accent' : 'text-destructive'} border-current`}
                    >
                      {n.ready ? <CheckCircle className="mr-1" size={12} /> : <XCircle className="mr-1" size={12} />}
                      {n.ready ? t('cn.ready') : t('cn.notReady')}
                    </Badge>
                    {n.unschedulable ? (
                      <Button
                        variant="outline"
                        size="sm"
                        className="font-mono gap-1.5"
                        disabled={busy === n.name}
                        onClick={() => setCordon(n, false)}
                      >
                        <LockSimpleOpen />
                        {t('cn.uncordon')}
                      </Button>
                    ) : (
                      <Button
                        variant="outline"
                        size="sm"
                        className="font-mono gap-1.5"
                        disabled={busy === n.name}
                        onClick={() => setCordonTarget(n)}
                      >
                        <LockSimple />
                        {t('cn.cordon')}
                      </Button>
                    )}
                    <Button
                      variant="outline"
                      size="sm"
                      className="font-mono gap-1.5"
                      disabled={busy === n.name}
                      onClick={() => setDrainTarget(n)}
                    >
                      <Eject />
                      {t('cn.drain')}
                    </Button>
                  </div>
                </div>
              </CardHeader>
              <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <Meter
                  label={t('cn.cpu')}
                  pct={cpuPct}
                  detail={
                    n.cpuUsedCores !== undefined
                      ? t('cn.coresOf', { used: n.cpuUsedCores.toFixed(2), total: n.cpuCores })
                      : t('cn.coresNoMetrics', { total: n.cpuCores })
                  }
                />
                <Meter
                  label={t('cn.memory')}
                  pct={memPct}
                  detail={
                    n.memUsedGiB !== undefined
                      ? `${n.memUsedGiB.toFixed(1)} / ${n.memGiB.toFixed(0)} GiB`
                      : t('cn.gibNoMetrics', { total: n.memGiB.toFixed(0) })
                  }
                />
              </CardContent>
            </Card>
          )
        })}

      <AlertDialog open={!!cordonTarget} onOpenChange={(open) => !open && setCordonTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('cn.cordonTitle', { name: cordonTarget?.name ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('cn.cordonDesc')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('cn.keepSchedulable')}</AlertDialogCancel>
            <AlertDialogAction onClick={() => cordonTarget && setCordon(cordonTarget, true)}>
              {t('cn.cordonNode')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!drainTarget} onOpenChange={(open) => !open && setDrainTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('cn.drainTitle', { name: drainTarget?.name ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('cn.drainDesc')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={() => drainTarget && drain(drainTarget)}>
              {t('cn.drainNode')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}


