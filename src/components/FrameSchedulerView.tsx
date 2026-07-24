import { SchedulingPolicy, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Calendar, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

export function FrameSchedulerView() {
  const { state, reload } = useLiveResource<{ items: SchedulingPolicy[] }>(() =>
    frame.scheduler.listPolicies(),
  )
  const policies = state.phase === 'ready' ? state.data.items : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Calendar className="text-primary" />
            Scheduling Policies
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
            Live <span className="font-mono">SchedulingPolicy</span> resources — scheduler backend,
            queues and gang/preemption settings reconciled by the operator.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No SchedulingPolicies defined." />

      {state.phase === 'ready' &&
        policies.map((p) => (
          <Card key={p.name}>
            <CardHeader>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div>
                  <CardTitle className="font-mono text-lg">{p.name}</CardTitle>
                  <p className="text-xs text-muted-foreground font-mono">
                    scheduler: {p.scheduler}
                  </p>
                </div>
                <Badge variant="outline" className="font-mono text-primary border-current">
                  queue {p.queue}
                </Badge>
              </div>
            </CardHeader>
            <CardContent className="flex flex-wrap gap-2 text-[10px] font-mono">
              <Badge variant="outline">weight {p.queueWeight}</Badge>
              <Badge variant="outline">priority {p.priority}</Badge>
              <Badge variant="outline" className={p.gangScheduling ? 'text-accent' : 'text-muted-foreground'}>
                gang {p.gangScheduling ? 'on' : 'off'}
              </Badge>
              <Badge variant="outline" className={p.preemption ? 'text-warning' : 'text-muted-foreground'}>
                preemption {p.preemption ? 'on' : 'off'}
              </Badge>
            </CardContent>
          </Card>
        ))}
    </div>
  )
}
