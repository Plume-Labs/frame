import { ClusterNodeInfo, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Cpu, ArrowClockwise, CheckCircle, XCircle } from '@phosphor-icons/react'

const frame = createFrameClient()

export function ClusterNodesView() {
  const { state, reload } = useLiveResource<ClusterNodeInfo[]>(() => frame.cluster.nodes())

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
                  <Badge
                    variant="outline"
                    className={`font-mono ${n.ready ? 'text-accent' : 'text-destructive'} border-current`}
                  >
                    {n.ready ? <CheckCircle className="mr-1" size={12} /> : <XCircle className="mr-1" size={12} />}
                    {n.ready ? 'Ready' : 'NotReady'}
                  </Badge>
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
