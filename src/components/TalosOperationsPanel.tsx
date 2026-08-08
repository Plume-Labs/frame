import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ArrowClockwise, Wrench } from '@phosphor-icons/react'

import { createFrameClient, frameListPath, type TalosOperation } from '@/lib/frame-sdk'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'

const frame = createFrameClient()

function tone(ready: TalosOperation['ready']): 'default' | 'destructive' | 'secondary' {
  if (ready === 'True') return 'default'
  if (ready === 'False') return 'destructive'
  return 'secondary'
}

/**
 * TalosMachineConfig and TalosUpgrade, the two CRDs that reconfigure and
 * upgrade a node's operating system.
 *
 * They had no control-plane surface at all: the operator reconciled them, the
 * CRDs shipped, and neither the SDK nor any screen mentioned them, so the only
 * way to see whether a machine-config patch landed was kubectl.
 *
 * Read-only for now. Both kinds are write-once — the controller talks Talos
 * gRPC and records the outcome in Ready — so the useful thing is the outcome,
 * and issuing one belongs with the provisioning wizard that already knows a
 * node's endpoint and secret.
 */
export function TalosOperationsPanel() {
  const { state, reload } = useLiveResource<TalosOperation[]>(
    () => frame.talos.list(),
    [],
    [frameListPath('talosmachineconfigs'), frameListPath('talosupgrades')],
  )
  const ops = state.phase === 'ready' ? state.data : []

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-xl flex items-center gap-2">
          <Wrench className="text-primary" />
          Talos Operations
          <Button variant="outline" size="sm" className="ml-auto font-mono" onClick={reload}>
            <ArrowClockwise className="mr-1" /> Refresh
          </Button>
        </CardTitle>
      </CardHeader>
      <CardContent>
        <LiveStates
          state={state}
          emptyLabel="No machine-config patch or upgrade has been requested."
        />

        {state.phase === 'ready' && ops.length > 0 && (
          <div className="space-y-2">
            {ops.map((op) => (
              <div
                key={`${op.kind}/${op.name}`}
                className="rounded border border-border p-3 font-mono text-xs space-y-1"
              >
                <div className="flex items-center gap-2">
                  <Badge variant={tone(op.ready)}>{op.reason}</Badge>
                  <span className="text-foreground">{op.name}</span>
                  <span className="text-muted-foreground">
                    {op.kind === 'TalosUpgrade' ? 'upgrade' : 'machine config'}
                  </span>
                  <span className="ml-auto text-muted-foreground">{op.nodeName}</span>
                </div>
                {op.image && <div className="text-muted-foreground">{op.image}</div>}
                {op.message && <div className="text-muted-foreground">{op.message}</div>}
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
