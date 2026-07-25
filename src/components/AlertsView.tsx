import { AlertsStatus, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Bell, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

const TONE: Record<string, string> = {
  critical: 'text-destructive',
  warning: 'text-warning',
  info: 'text-accent',
  none: 'text-muted-foreground',
}

export function AlertsView() {
  const { state, reload } = useLiveResource<AlertsStatus | null>(() => frame.cluster.alerts())
  const data = state.phase === 'ready' ? state.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Bell className="text-primary" />
            Active alerts (Alertmanager)
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {data && (
          <CardContent className="flex flex-wrap items-center gap-3">
            <div className="mr-4">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Firing</div>
              <div className="font-mono font-bold text-2xl text-foreground">{data.alerts.length}</div>
            </div>
            {Object.entries(data.bySeverity)
              .sort()
              .map(([s, n]) => (
                <Badge key={s} variant="outline" className={`font-mono text-[11px] border-current ${TONE[s] ?? 'text-foreground'}`}>
                  {n} {s}
                </Badge>
              ))}
            <span className="text-[11px] text-muted-foreground font-mono">Prometheus rules + routed Falco detections</span>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No active alerts (or Alertmanager not deployed)." />

      {data && data.alerts.length > 0 && (
        <Card>
          <CardContent className="pt-6 space-y-2">
            {data.alerts.map((a, i) => (
              <div key={`${a.name}-${a.namespace}-${i}`} className="flex flex-wrap items-center gap-2 rounded border border-border bg-secondary/30 px-3 py-2">
                <Badge variant="outline" className={`font-mono text-[10px] border-current ${TONE[a.severity] ?? 'text-foreground'}`}>{a.severity}</Badge>
                <span className="font-mono text-xs font-bold">{a.name}</span>
                {a.namespace && <span className="font-mono text-[10px] text-muted-foreground">{a.namespace}</span>}
                <span className="font-mono text-[10px] text-muted-foreground flex-1 truncate">{a.summary}</span>
                <span className="font-mono text-[10px] text-muted-foreground">{a.startsAt ? new Date(a.startsAt).toLocaleTimeString() : ''}</span>
              </div>
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
