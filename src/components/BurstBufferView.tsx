import { BurstNode, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { HardDrive, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()
const GiB = 1024 ** 3

export function BurstBufferView() {
  const { state, reload } = useLiveResource<BurstNode[]>(() => frame.cluster.burstBuffer())
  const nodes = state.phase === 'ready' ? state.data : []
  const total = nodes.reduce((s, n) => s + n.totalBytes, 0)
  const used = nodes.reduce((s, n) => s + n.usedBytes, 0)

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrive className="text-primary" />
            Burst Buffer — SSD scratch tier
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && (
          <CardContent className="grid grid-cols-2 sm:grid-cols-3 gap-4">
            <Stat label="Nodes with burst" value={`${nodes.length}`} />
            <Stat label="Total capacity" value={`${(total / GiB).toFixed(1)} GiB`} />
            <Stat label="Staged" value={`${(used / GiB).toFixed(2)} GiB`} />
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No /burst-buffer mount found on any node." />

      {state.phase === 'ready' &&
        nodes.map((n) => {
          const pct = n.totalBytes ? (n.usedBytes / n.totalBytes) * 100 : 0
          return (
            <Card key={n.node}>
              <CardHeader>
                <CardTitle className="font-mono text-lg">{n.node}</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1">
                <Progress value={pct} className="h-2" />
                <div className="flex justify-between text-xs text-muted-foreground font-mono">
                  <span>{(n.usedBytes / GiB).toFixed(2)} GiB staged</span>
                  <span>{(n.totalBytes / GiB).toFixed(1)} GiB SSD</span>
                </div>
              </CardContent>
            </Card>
          )
        })}
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className="font-mono text-2xl font-bold text-foreground">{value}</div>
    </div>
  )
}
