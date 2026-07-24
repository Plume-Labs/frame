import { VolcanoStats, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ArrowsLeftRight, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

const phaseTone = (p: string) =>
  p === 'Running' ? 'text-accent' : p === 'Pending' || p === 'Inqueue' ? 'text-warning' : 'text-muted-foreground'

export function VolcanoPoolsView() {
  const { state, reload } = useLiveResource<VolcanoStats>(() => frame.cluster.volcano())
  const d = state.phase === 'ready' ? state.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ArrowsLeftRight className="text-primary" />
            Elastic Pools — Volcano Queues
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
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Live Volcano scheduling queues (elastic resource pools) and the gang-scheduled
            PodGroups running in them.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="Volcano not installed / no queues." />

      {d && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {d.queues.map((q) => {
            const groups = d.podGroups.filter((p) => p.queue === q.name)
            return (
              <Card key={q.name}>
                <CardHeader>
                  <div className="flex items-center justify-between gap-2">
                    <CardTitle className="font-mono text-lg">{q.name}</CardTitle>
                    <Badge variant="outline" className="font-mono text-[10px] text-accent border-current">
                      {q.state}
                    </Badge>
                  </div>
                </CardHeader>
                <CardContent className="space-y-2 text-xs font-mono">
                  <Row label="Weight" value={`${q.weight}`} />
                  <Row label="Capability" value={`${q.cpuCapability} CPU · ${q.memCapability}`} />
                  <Row label="Reclaimable" value={q.reclaimable ? 'yes' : 'no'} />
                  <Row label="Running" value={`${q.running}`} />
                  {groups.length > 0 && (
                    <div className="pt-2 space-y-1">
                      {groups.map((g) => (
                        <div key={`${g.namespace}/${g.name}`} className="flex items-center justify-between">
                          <span className="text-muted-foreground truncate">
                            {g.namespace}/{g.name.slice(0, 20)}
                          </span>
                          <span className={`${phaseTone(g.phase)}`}>
                            {g.phase} ·{g.minMember}
                          </span>
                        </div>
                      ))}
                    </div>
                  )}
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-muted-foreground">{label}</span>
      <Badge variant="outline" className="font-mono text-[10px]">
        {value}
      </Badge>
    </div>
  )
}
