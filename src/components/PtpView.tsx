import { PtpNode, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Clock, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

const usFmt = (s: number) => `${(s * 1e6).toFixed(2)} µs`

export function PtpView() {
  const { state, reload } = useLiveResource<PtpNode[]>(() => frame.cluster.ptp())
  const nodes = state.phase === 'ready' ? state.data : []
  const synced = nodes.filter((n) => n.synced).length
  const maxOff = nodes.reduce((m, n) => Math.max(m, Math.abs(n.offsetSeconds)), 0)

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Clock className="text-primary" />
            Clock Sync — PTP / adjtimex
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && (
          <CardContent className="grid grid-cols-2 sm:grid-cols-3 gap-4">
            <Stat label="Synced nodes" value={`${synced} / ${nodes.length}`} tone="accent" />
            <Stat label="Max offset" value={usFmt(maxOff)} />
            <Stat label="Source" value="ptp_kvm PHC" small />
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="node-exporter (timex) not deployed." />

      {state.phase === 'ready' && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg">Per-node</CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {nodes.map((n) => (
              <div key={n.node} className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-xs font-bold truncate">{n.node}</span>
                  <Badge variant="outline" className={`text-[10px] font-mono ${n.synced ? 'text-accent' : 'text-destructive'}`}>
                    {n.synced ? 'SYNCED' : 'UNSYNCED'}
                  </Badge>
                </div>
                <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground font-mono">
                  <div>
                    <div className="text-foreground font-bold">{usFmt(n.offsetSeconds)}</div>
                    offset
                  </div>
                  <div>
                    <div className="text-foreground font-bold">{n.freqPpm.toFixed(1)}</div>
                    ppm adj
                  </div>
                  <div>
                    <div className="text-foreground font-bold">{usFmt(n.maxErrorSeconds)}</div>
                    max err
                  </div>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function Stat({ label, value, tone, small }: { label: string; value: string; tone?: 'accent'; small?: boolean }) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className={`font-mono font-bold ${small ? 'text-sm' : 'text-2xl'} ${tone === 'accent' ? 'text-accent' : 'text-foreground'}`}>
        {value}
      </div>
    </div>
  )
}
