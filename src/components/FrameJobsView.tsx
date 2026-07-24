import { Job, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Queue, ArrowClockwise, CheckCircle, Spinner, XCircle, Clock } from '@phosphor-icons/react'

const frame = createFrameClient()

const STATUS: Record<Job['status'], { tone: string; icon: React.ReactNode }> = {
  running: { tone: 'text-primary', icon: <Spinner className="animate-spin" size={12} /> },
  completed: { tone: 'text-accent', icon: <CheckCircle size={12} /> },
  failed: { tone: 'text-destructive', icon: <XCircle size={12} /> },
  queued: { tone: 'text-muted-foreground', icon: <Clock size={12} /> },
}

export function FrameJobsView() {
  const { state, reload } = useLiveResource<{ items: Job[] }>(() => frame.jobs.list())
  const jobs = state.phase === 'ready' ? state.data.items : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Queue className="text-primary" />
            Frame Jobs
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
            Live <span className="font-mono">FrameJob</span> resources reconciled by the operator.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No FrameJobs submitted." />

      {state.phase === 'ready' &&
        jobs.map((j) => {
          const s = STATUS[j.status]
          return (
            <Card key={j.id}>
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div className="min-w-0">
                    <CardTitle className="font-mono text-lg truncate">{j.name}</CardTitle>
                    <p className="text-xs text-muted-foreground font-mono">
                      {j.pipeline} · {j.namespace}
                    </p>
                  </div>
                  <Badge variant="outline" className={`font-mono ${s.tone} border-current gap-1`}>
                    {s.icon}
                    {j.status}
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="flex flex-wrap gap-2 text-[10px] font-mono">
                <Badge variant="outline">class {j.serviceClass}</Badge>
                <Badge variant="outline">priority {j.priority}</Badge>
                <Badge variant="outline">{j.gpuCount} GPU</Badge>
                <Badge variant="outline">
                  created {new Date(j.createdAt).toLocaleString()}
                </Badge>
              </CardContent>
            </Card>
          )
        })}
    </div>
  )
}
