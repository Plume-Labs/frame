import { KsmStats, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Archive, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

export function KsmView() {
  const { state, reload } = useLiveResource<KsmStats>(() => frame.cluster.ksm())
  const s = state.phase === 'ready' ? state.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Archive className="text-primary" />
            KSM — Kernel Same-page Merging
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
        {s && (
          <CardContent className="grid grid-cols-2 sm:grid-cols-3 gap-4">
            <Stat label="Enabled nodes" value={`${s.enabledNodes} / ${s.nodes.length}`} tone="accent" />
            <Stat label="Memory saved" value={`${s.totalSavedMiB.toFixed(1)} MiB`} />
            <Stat label="Pages sharing" value={s.totalPagesSharing.toLocaleString()} />
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="node-exporter (ksmd) not deployed." />

      {s && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg">Per-node</CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {s.nodes.map((n) => (
              <div key={n.node} className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-xs font-bold truncate">{n.node}</span>
                  <Badge
                    variant="outline"
                    className={`text-[10px] font-mono ${n.run ? 'text-accent' : 'text-muted-foreground'}`}
                  >
                    KSM {n.run ? 'ON' : 'OFF'}
                  </Badge>
                </div>
                <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground font-mono">
                  <div>
                    <div className="text-foreground font-bold">{n.savedMiB.toFixed(1)} MiB</div>
                    saved
                  </div>
                  <div>
                    <div className="text-foreground font-bold">{n.pagesSharing.toLocaleString()}</div>
                    sharing
                  </div>
                  <div>
                    <div className="text-foreground font-bold">{n.fullScans}</div>
                    scans
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

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'accent' }) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className={`font-mono text-2xl font-bold ${tone === 'accent' ? 'text-accent' : 'text-foreground'}`}>
        {value}
      </div>
    </div>
  )
}
