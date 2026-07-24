import { Rack, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Stack, ArrowClockwise, Cpu, CheckCircle, XCircle, HardDrives } from '@phosphor-icons/react'

const frame = createFrameClient()

export function RacksView() {
  const { state, reload } = useLiveResource<Rack[]>(() => frame.cluster.racks())
  const racks = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Stack className="text-primary" />
            Racks
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Real nodes grouped by physical topology — the <span className="font-mono">topology.frame.io/rack</span>{' '}
            label (the hypervisor host each node runs on). Shared host = shared failure domain and oversubscribed cores.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No nodes / racks found." />

      {state.phase === 'ready' &&
        racks.map((r) => (
          <Card key={r.name}>
            <CardHeader>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="flex items-center gap-3">
                  <Stack className="text-primary" size={22} weight="duotone" />
                  <div>
                    <CardTitle className="font-mono text-lg">{r.name}</CardTitle>
                    <p className="text-xs text-muted-foreground font-mono">
                      {r.nodes.length} nodes · {r.totalCpu.toFixed(0)} vCPU · {r.totalMem.toFixed(0)} GiB · {r.totalPods} pods
                    </p>
                  </div>
                </div>
                <Badge variant="outline" className={`font-mono ${r.readyNodes === r.nodes.length ? 'text-accent' : 'text-warning'} border-current`}>
                  {r.readyNodes}/{r.nodes.length} ready
                </Badge>
              </div>
              {r.physical && (
                <div className="mt-2 flex flex-wrap items-center gap-2 text-[11px] font-mono">
                  <Badge variant="secondary" className="gap-1">
                    <HardDrives size={12} />{r.physical.hypervisor} host · {r.physical.pcpu} pCPU · {r.physical.pmemGiB} GiB
                  </Badge>
                  {r.physical.pcpu > 0 && (
                    <span className={r.totalCpu > r.physical.pcpu ? 'text-warning' : 'text-muted-foreground'}>
                      vCPU {r.totalCpu.toFixed(0)}/{r.physical.pcpu} ({(r.totalCpu / r.physical.pcpu).toFixed(1)}× oversubscribed)
                    </span>
                  )}
                </div>
              )}
            </CardHeader>
            <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {r.nodes.map((n) => (
                <div key={n.name} className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-xs font-bold truncate flex items-center gap-1">
                      <Cpu size={12} className="text-primary" />
                      {n.name}
                    </span>
                    {n.ready ? <CheckCircle size={13} className="text-accent" /> : <XCircle size={13} className="text-destructive" />}
                  </div>
                  <div className="text-[10px] text-muted-foreground font-mono">{n.role}</div>
                  <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground font-mono">
                    <div><div className="text-foreground font-bold">{n.cpuCores}</div>vCPU</div>
                    <div><div className="text-foreground font-bold">{n.memGiB.toFixed(0)}</div>GiB</div>
                    <div><div className="text-foreground font-bold">{n.pods}</div>pods</div>
                  </div>
                </div>
              ))}
            </CardContent>
          </Card>
        ))}
    </div>
  )
}
