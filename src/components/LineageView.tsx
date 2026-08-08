import { WorkflowTrace, createFrameClient, crdListPath } from '@/lib/frame-sdk'
import { config } from '@/lib/frame-config'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { GitBranch, ArrowClockwise, ArrowRight } from '@phosphor-icons/react'

const frame = createFrameClient()

const phaseTone = (p: string) =>
  p === 'Succeeded' ? 'text-accent' : p === 'Running' ? 'text-primary' : p === 'Failed' || p === 'Error' ? 'text-destructive' : 'text-warning'

const dur = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${ms}ms`)

export function LineageView() {
  const { state, reload } = useLiveResource<WorkflowTrace[]>(
    () => frame.cluster.workflows(),
    [],
    [crdListPath('argoproj.io', 'v1alpha1', 'workflows', config().namespaces.argo)],
  )
  const traces = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <GitBranch className="text-primary" />
            Pipeline Lineage
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
            Live Argo Workflow runs — each DAG step as a timed span, in execution order.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No Argo Workflows found." />

      {state.phase === 'ready' &&
        traces.map((t) => {
          const maxDur = Math.max(1, ...t.spans.map((s) => s.durationMs))
          return (
            <Card key={t.name}>
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div>
                    <CardTitle className="font-mono text-lg">{t.name}</CardTitle>
                    <p className="text-xs text-muted-foreground font-mono">
                      {t.spans.length} steps · {dur(t.totalDurationMs)} total
                    </p>
                  </div>
                  <Badge variant="outline" className={`font-mono ${phaseTone(t.phase)} border-current`}>
                    {t.phase}
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="space-y-3">
                {/* lineage chain */}
                <div className="flex flex-wrap items-center gap-1">
                  {t.spans.map((s, i) => (
                    <span key={s.name} className="flex items-center gap-1">
                      <Badge variant="outline" className={`font-mono text-[10px] ${phaseTone(s.phase)} border-current`}>
                        {s.name}
                      </Badge>
                      {i < t.spans.length - 1 && <ArrowRight size={11} className="text-muted-foreground" />}
                    </span>
                  ))}
                </div>
                {/* span Gantt — bars offset by real start time, so sequential
                    steps cascade instead of all starting at the left. */}
                <div className="space-y-1">
                  {t.spans.map((s) => {
                    const span0 = t.startedAt ? new Date(t.startedAt).getTime() : 0
                    const total = t.totalDurationMs || maxDur
                    const offset = s.startedAt && span0 ? ((new Date(s.startedAt).getTime() - span0) / total) * 100 : 0
                    const width = (s.durationMs / total) * 100
                    return (
                      <div key={s.name} className="flex items-center gap-2">
                        <span className="font-mono text-[10px] w-40 truncate text-muted-foreground">{s.name}</span>
                        <div className="flex-1 h-3 bg-secondary/40 rounded overflow-hidden relative">
                          <div
                            className={`h-full rounded absolute ${
                              s.phase === 'Succeeded' ? 'bg-accent' : s.phase === 'Running' ? 'bg-primary' : 'bg-destructive'
                            }`}
                            style={{ left: `${Math.min(98, Math.max(0, offset))}%`, width: `${Math.max(2, Math.min(100 - offset, width))}%` }}
                          />
                        </div>
                        <span className="font-mono text-[10px] w-14 text-right text-muted-foreground">{dur(s.durationMs)}</span>
                      </div>
                    )
                  })}
                </div>
              </CardContent>
            </Card>
          )
        })}
    </div>
  )
}
