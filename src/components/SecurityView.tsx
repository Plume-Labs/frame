import { SecurityStatus, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ShieldWarning, ArrowClockwise, Warning } from '@phosphor-icons/react'

const frame = createFrameClient()

// Falco priority → tailwind text colour. Lower rank = more severe.
const TONE: Record<string, string> = {
  emergency: 'text-destructive',
  alert: 'text-destructive',
  critical: 'text-destructive',
  error: 'text-warning',
  warning: 'text-warning',
  notice: 'text-accent',
  informational: 'text-muted-foreground',
  debug: 'text-muted-foreground',
}

export function SecurityView() {
  const { state, reload } = useLiveResource<SecurityStatus | null>(() => frame.cluster.security())
  const sec = state.phase === 'ready' ? state.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ShieldWarning className="text-primary" />
            Security — runtime threats (Falco)
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {sec && (
          <CardContent className="flex flex-wrap items-center gap-3">
            <div className="mr-4">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Detections</div>
              <div className="font-mono font-bold text-2xl text-foreground">{sec.total}</div>
            </div>
            {Object.entries(sec.byPriority)
              .sort()
              .map(([p, n]) => (
                <Badge key={p} variant="outline" className={`font-mono text-[11px] border-current ${TONE[p.toLowerCase()] ?? 'text-foreground'}`}>
                  {n} {p}
                </Badge>
              ))}
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No Falco detections (or Falco not deployed)." />

      {sec && sec.events.length > 0 && (
        <Card>
          <CardHeader><CardTitle className="font-mono text-base">Detections by rule</CardTitle></CardHeader>
          <CardContent className="space-y-2">
            {sec.events.map((e, i) => (
              <div key={`${e.rule}-${e.pod}-${i}`} className="flex flex-wrap items-center gap-2 rounded border border-border bg-secondary/30 px-3 py-2">
                <Warning size={14} className={TONE[e.priority.toLowerCase()] ?? 'text-foreground'} />
                <span className="font-mono text-xs font-bold flex-1 min-w-[12rem]">{e.rule}</span>
                <Badge variant="outline" className={`font-mono text-[10px] border-current ${TONE[e.priority.toLowerCase()] ?? 'text-foreground'}`}>{e.priority}</Badge>
                <span className="font-mono text-[10px] text-muted-foreground">{e.source}</span>
                <span className="font-mono text-[10px] text-muted-foreground truncate max-w-[16rem]">
                  {e.namespace ? `${e.namespace}/${e.pod}` : e.node}
                </span>
                <Badge variant="secondary" className="font-mono text-[10px]">×{e.count}</Badge>
              </div>
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  )
}
