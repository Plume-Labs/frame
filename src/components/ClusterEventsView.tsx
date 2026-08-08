import { ClusterEvent, createFrameClient, coreListPath } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Info, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

export function ClusterEventsView() {
  const { state, reload } = useLiveResource<ClusterEvent[]>(
    () => frame.cluster.events(),
    [],
    [coreListPath('events')],
  )
  const events = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Info className="text-primary" />
            Cluster Events
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
        {state.phase === 'ready' && (
          <CardContent>
            <ScrollArea className="h-[520px]">
              <div className="space-y-1">
                {events.map((e, i) => (
                  <div
                    key={`${e.involvedObject}-${e.reason}-${i}`}
                    className="flex items-start gap-3 p-2 rounded border border-border/50 bg-secondary/20"
                  >
                    <Badge
                      variant="outline"
                      className={`shrink-0 text-[10px] font-mono ${
                        e.type === 'Warning' ? 'text-warning border-warning/40' : 'text-muted-foreground'
                      }`}
                    >
                      {e.reason}
                    </Badge>
                    <div className="min-w-0 flex-1">
                      <div className="text-xs font-mono text-foreground break-words">{e.message}</div>
                      <div className="text-[10px] text-muted-foreground font-mono mt-0.5">
                        {e.namespace ? `${e.namespace}/` : ''}
                        {e.involvedObject}
                        {e.count > 1 ? ` ·×${e.count}` : ''}
                        {e.lastTimestamp ? ` · ${new Date(e.lastTimestamp).toLocaleTimeString()}` : ''}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </ScrollArea>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No recent events." />
    </div>
  )
}
