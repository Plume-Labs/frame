import { useState } from 'react'
import { toast } from 'sonner'
import { ClusterNodeInfo, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
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
import { Cpu, ArrowClockwise, CheckCircle, XCircle, LockSimple, LockSimpleOpen, Eject } from '@phosphor-icons/react'

const frame = createFrameClient()

export function ClusterNodesView() {
  const { state, reload } = useLiveResource<ClusterNodeInfo[]>(() => frame.cluster.nodes())
  const [cordonTarget, setCordonTarget] = useState<ClusterNodeInfo | null>(null)
  const [drainTarget, setDrainTarget] = useState<ClusterNodeInfo | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  async function setCordon(node: ClusterNodeInfo, unschedulable: boolean) {
    setBusy(node.name)
    try {
      await frame.cluster.cordon(node.name, unschedulable)
      toast.success(`${node.name} ${unschedulable ? 'cordoned' : 'uncordoned'}`)
      reload()
    } catch (e) {
      toast.error(`Failed to ${unschedulable ? 'cordon' : 'uncordon'} ${node.name}`, {
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
      const detail = `${evicted} evicted · ${skipped} skipped (DaemonSet/static)${
        blocked ? ` · ${blocked} blocked by PodDisruptionBudget` : ''
      }`
      // A blocked pod is not a failure of the drain, but it does mean the node
      // is not actually empty — say so rather than showing a plain success.
      if (blocked) toast.warning(`${node.name} partially drained`, { description: detail })
      else toast.success(`${node.name} drained`, { description: detail })
      reload()
    } catch (e) {
      toast.error(`Failed to drain ${node.name}`, {
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
            Cluster Nodes
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && (
          <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <Stat label="Nodes ready" value={`${ready} / ${nodes.length}`} tone="accent" />
            <Stat
              label="CPU in use"
              value={`${usedCpu.toFixed(1)} / ${totalCpu.toFixed(0)} cores`}
            />
            <Stat
              label="Memory in use"
              value={`${usedMem.toFixed(1)} / ${totalMem.toFixed(0)} GiB`}
            />
            <Stat label="Kubelet" value={nodes[0]?.kubeletVersion ?? '—'} size="sm" />
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No nodes returned by the cluster." />

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
                        Cordoned
                      </Badge>
                    )}
                    <Badge
                      variant="outline"
                      className={`font-mono ${n.ready ? 'text-accent' : 'text-destructive'} border-current`}
                    >
                      {n.ready ? <CheckCircle className="mr-1" size={12} /> : <XCircle className="mr-1" size={12} />}
                      {n.ready ? 'Ready' : 'NotReady'}
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
                        Uncordon
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
                        Cordon
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
                      Drain
                    </Button>
                  </div>
                </div>
              </CardHeader>
              <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <Meter
                  label="CPU"
                  pct={cpuPct}
                  detail={
                    n.cpuUsedCores !== undefined
                      ? `${n.cpuUsedCores.toFixed(2)} / ${n.cpuCores} cores`
                      : `${n.cpuCores} cores (no metrics)`
                  }
                />
                <Meter
                  label="Memory"
                  pct={memPct}
                  detail={
                    n.memUsedGiB !== undefined
                      ? `${n.memUsedGiB.toFixed(1)} / ${n.memGiB.toFixed(0)} GiB`
                      : `${n.memGiB.toFixed(0)} GiB (no metrics)`
                  }
                />
              </CardContent>
            </Card>
          )
        })}

      <AlertDialog open={!!cordonTarget} onOpenChange={(open) => !open && setCordonTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Cordon {cordonTarget?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Marks the node unschedulable — no new pods will be placed on it. Existing pods keep running
              until evicted separately. Reversible with Uncordon.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep schedulable</AlertDialogCancel>
            <AlertDialogAction onClick={() => cordonTarget && setCordon(cordonTarget, true)}>
              Cordon node
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!drainTarget} onOpenChange={(open) => !open && setDrainTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Drain {drainTarget?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Cordons the node, then evicts every pod on it so its workloads reschedule elsewhere.
              DaemonSet and static pods stay — their controllers would recreate them here anyway.
              Pods protected by a PodDisruptionBudget may be refused and left running. Undo by
              uncordoning; evicted pods do not come back to this node on their own.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => drainTarget && drain(drainTarget)}>
              Drain node
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Stat({
  label,
  value,
  tone,
  size,
}: {
  label: string
  value: string
  tone?: 'accent'
  size?: 'sm'
}) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      <div
        className={`font-mono font-bold ${size === 'sm' ? 'text-sm' : 'text-2xl'} ${
          tone === 'accent' ? 'text-accent' : 'text-foreground'
        }`}
      >
        {value}
      </div>
    </div>
  )
}

function Meter({ label, pct, detail }: { label: string; pct: number; detail: string }) {
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs font-mono">
        <span className="text-muted-foreground">{label}</span>
        <span className="text-foreground">{pct.toFixed(0)}%</span>
      </div>
      <Progress value={Math.max(0, Math.min(100, pct))} className="h-2" />
      <div className="text-[10px] text-muted-foreground font-mono">{detail}</div>
    </div>
  )
}
