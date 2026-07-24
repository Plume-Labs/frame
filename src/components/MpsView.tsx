import { GpuInfo, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Gauge, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

/**
 * MPS (NVIDIA Multi-Process Service) sharing. Reads the real GPU inventory and
 * reports the actual sharing config from the device-plugin. On this cluster
 * the GPU is exclusive (no MPS/time-slicing), so we say so rather than fake it.
 */
export function MpsView() {
  const { state, reload } = useLiveResource<GpuInfo[]>(() => frame.cluster.gpus())
  const gpus = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Gauge className="text-primary" />
            MPS — GPU sharing
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Multi-Process Service lets several processes share one GPU context. The device-plugin
            here advertises each GPU as a single exclusive resource — MPS / time-slicing is not enabled.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No GPUs present." />

      {state.phase === 'ready' &&
        gpus.map((g) => (
          <Card key={`${g.node}-${g.index}`}>
            <CardHeader>
              <div className="flex items-center justify-between">
                <CardTitle className="font-mono text-lg">{g.model} #{g.index}</CardTitle>
                <Badge variant="outline" className="font-mono text-[10px]">exclusive · MPS off</Badge>
              </div>
              <p className="text-xs text-muted-foreground font-mono">{g.node}</p>
            </CardHeader>
            <CardContent className="grid grid-cols-3 gap-4 text-xs font-mono">
              <div><div className="text-muted-foreground">Advertised</div><div className="text-foreground font-bold">1 × nvidia.com/gpu</div></div>
              <div><div className="text-muted-foreground">Clients</div><div className="text-foreground font-bold">1 (exclusive)</div></div>
              <div><div className="text-muted-foreground">Util now</div><div className="text-foreground font-bold">{g.utilPct}%</div></div>
            </CardContent>
          </Card>
        ))}
    </div>
  )
}
