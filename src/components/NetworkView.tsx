import { NetNode, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Network, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

const GiB = 1024 ** 3
const fmt = (b: number) => (b >= GiB ? `${(b / GiB).toFixed(2)} GiB` : `${(b / 1024 ** 2).toFixed(0)} MiB`)

export function NetworkView() {
  const { state, reload } = useLiveResource<NetNode[]>(() => frame.cluster.network())
  const nodes = state.phase === 'ready' ? state.data : []
  const rx = nodes.reduce((s, n) => s + n.rxBytes, 0)
  const tx = nodes.reduce((s, n) => s + n.txBytes, 0)

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Network className="text-primary" />
            Network
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
          <CardContent className="grid grid-cols-2 sm:grid-cols-3 gap-4">
            <Stat label="Primary NIC" value={nodes[0]?.device ?? 'eth0'} small />
            <Stat label="Total received" value={fmt(rx)} />
            <Stat label="Total transmitted" value={fmt(tx)} />
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="node-exporter (netdev) not deployed." />

      {state.phase === 'ready' && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg">Per-node ({nodes[0]?.device})</CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {nodes.map((n) => (
              <div key={n.node} className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2">
                <span className="font-mono text-xs font-bold truncate block">{n.node}</span>
                <div className="grid grid-cols-2 gap-2 text-[10px] text-muted-foreground font-mono">
                  <div>
                    <div className="text-accent font-bold">{fmt(n.rxBytes)}</div>
                    received
                  </div>
                  <div>
                    <div className="text-primary font-bold">{fmt(n.txBytes)}</div>
                    transmitted
                  </div>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      <Badge variant="outline" className="font-mono text-[10px] text-muted-foreground">
        cumulative counters since boot · RDMA/SR-IOV/DPDK require dedicated NICs (not present)
      </Badge>
    </div>
  )
}

function Stat({ label, value, small }: { label: string; value: string; small?: boolean }) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className={`font-mono font-bold text-foreground ${small ? 'text-sm' : 'text-2xl'}`}>{value}</div>
    </div>
  )
}
