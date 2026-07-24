import { Resilience, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ShieldWarning, ArrowClockwise, Database, ShieldCheck } from '@phosphor-icons/react'

const frame = createFrameClient()

export function ResilienceView() {
  const { state, reload } = useLiveResource<Resilience>(() => frame.cluster.resilience())
  const r = state.phase === 'ready' ? state.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ShieldWarning className="text-primary" />
            Resilience
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
            Live reliability posture — data durability, disruption budgets and restart hotspots.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No resilience data." />

      {r && (
        <>
          <Card>
            <CardHeader>
              <CardTitle className="font-mono text-lg flex items-center gap-2">
                <Database className="text-primary" />
                Data durability (Ceph)
              </CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4">
              <Stat label="Ceph health" value={r.cephHealth.replace('HEALTH_', '')} tone={r.cephHealth === 'HEALTH_OK' ? 'accent' : 'warning'} />
              <Stat label="OSDs up" value={`${r.cephOsds}`} />
              <Stat label="Replication" value={r.cephReplication ? `×${r.cephReplication}` : '—'} />
              <Stat label="Total restarts" value={`${r.totalRestarts}`} tone={r.totalRestarts ? 'warning' : 'accent'} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="font-mono text-lg flex items-center gap-2">
                <ShieldCheck className="text-primary" />
                Disruption budgets
                {r.pdbAtRisk > 0 && (
                  <Badge variant="outline" className="ml-auto font-mono text-warning border-warning/40">
                    {r.pdbAtRisk} at risk
                  </Badge>
                )}
              </CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {r.pdbs.map((p) => (
                <div key={`${p.namespace}/${p.name}`} className="p-3 rounded-lg border border-border bg-secondary/30 space-y-1">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-xs font-bold truncate">{p.name}</span>
                    <Badge variant="outline" className={`text-[10px] font-mono ${p.disruptionsAllowed > 0 ? 'text-accent' : 'text-warning'}`}>
                      {p.disruptionsAllowed} allowed
                    </Badge>
                  </div>
                  <div className="text-[10px] text-muted-foreground font-mono">
                    {p.namespace} · {p.currentHealthy}/{p.desiredHealthy} healthy
                  </div>
                </div>
              ))}
            </CardContent>
          </Card>

          {r.hotspots.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle className="font-mono text-lg">Restart hotspots</CardTitle>
              </CardHeader>
              <CardContent className="space-y-1">
                {r.hotspots.map((h) => (
                  <div key={`${h.namespace}/${h.pod}`} className="flex items-center justify-between text-xs font-mono">
                    <span className="text-muted-foreground truncate">{h.namespace}/{h.pod}</span>
                    <Badge variant="outline" className="text-[10px] text-warning border-warning/40">
                      {h.restarts} restarts
                    </Badge>
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </>
      )}
    </div>
  )
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: 'accent' | 'warning' }) {
  const c = tone === 'warning' ? 'text-warning' : tone === 'accent' ? 'text-accent' : 'text-foreground'
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className={`font-mono text-2xl font-bold ${c}`}>{value}</div>
    </div>
  )
}
