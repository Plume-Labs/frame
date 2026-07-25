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
  const { state: nsState } = useLiveResource(() => frame.cluster.neuraSandboxJobs())
  const ns = nsState.phase === 'ready' ? nsState.data : null

  return (
    <div className="space-y-6">
      {ns && (ns.recent.length > 0 || Object.keys(ns.byPhase).length > 0) && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-base flex items-center gap-2">
              <Queue className="text-primary" size={18} /> Neura cluster jobs (sandbox pods)
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            <div className="flex flex-wrap gap-2 text-[11px] font-mono">
              {Object.entries(ns.byPhase).map(([p, n]) => (
                <Badge key={p} variant="outline" className={`border-current ${p === 'Running' ? 'text-primary' : p === 'Failed' ? 'text-destructive' : p === 'Succeeded' ? 'text-accent' : 'text-muted-foreground'}`}>{n} {p}</Badge>
              ))}
              {ns.recent.length === 0 && <span className="text-muted-foreground">no active sandbox jobs</span>}
            </div>
            {ns.recent.map((r) => (
              <div key={r.name} className="flex items-center gap-2 text-[11px] font-mono">
                <span className="flex-1 truncate">{r.name}</span>
                <span className="text-muted-foreground">{r.node}</span>
                <Badge variant="secondary" className="text-[10px]">{r.phase}</Badge>
              </div>
            ))}
          </CardContent>
        </Card>
      )}
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
