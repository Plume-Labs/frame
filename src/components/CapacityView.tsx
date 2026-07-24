import { CapacityResource, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ChartLine, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

const pct = (a: number, b: number) => (b > 0 ? (a / b) * 100 : 0)
const tone = (p: number) => (p >= 90 ? 'text-destructive' : p >= 75 ? 'text-warning' : 'text-accent')

export function CapacityView() {
  const { state, reload } = useLiveResource<CapacityResource[]>(() => frame.cluster.capacity())
  const res = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ChartLine className="text-primary" />
            Cluster Capacity
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
            Live allocatable capacity vs actual usage (metrics-server) and reserved requests. No
            forecast — that needs a metrics history store (Prometheus).
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No capacity data." />

      {state.phase === 'ready' &&
        res.map((r) => {
          const usedPct = pct(r.used, r.allocatable)
          const reqPct = pct(r.requested, r.allocatable)
          return (
            <Card key={r.name}>
              <CardHeader>
                <div className="flex items-center justify-between">
                  <CardTitle className="font-mono text-lg">{r.name}</CardTitle>
                  <Badge variant="outline" className={`font-mono ${tone(reqPct)} border-current`}>
                    {reqPct.toFixed(0)}% reserved
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="space-y-4">
                <Bar
                  label="Used (live)"
                  value={usedPct}
                  detail={`${r.used.toFixed(1)} / ${r.allocatable.toFixed(1)} ${r.unit}`}
                />
                <Bar
                  label="Requested (reserved)"
                  value={reqPct}
                  detail={`${r.requested.toFixed(1)} / ${r.allocatable.toFixed(1)} ${r.unit}`}
                />
                <div className="text-xs text-muted-foreground font-mono">
                  Headroom: {(r.allocatable - r.requested).toFixed(1)} {r.unit} unreserved ·{' '}
                  {(r.allocatable - r.used).toFixed(1)} {r.unit} unused
                </div>
              </CardContent>
            </Card>
          )
        })}
    </div>
  )
}

function Bar({ label, value, detail }: { label: string; value: number; detail: string }) {
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs font-mono">
        <span className="text-muted-foreground">{label}</span>
        <span className={tone(value)}>{value.toFixed(0)}%</span>
      </div>
      <Progress value={Math.min(100, value)} className="h-2" />
      <div className="text-[10px] text-muted-foreground font-mono">{detail}</div>
    </div>
  )
}
