import { AlluxioStats, NodePlacement, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { HardDrives, ArrowClockwise, Stack, Lightning } from '@phosphor-icons/react'

const frame = createFrameClient()

const GiB = 1024 ** 3
const TIER_DETAIL: Record<string, string> = {
  MEM: 'in-memory (fastest)',
  SSD: 'local NVMe/SSD',
  HDD: 'under-store (object/HDD)',
}

/** Alluxio stacked storage tiers + cache hit-rate — the caching layer in front
 *  of the DBs/object store. Renders nothing (silent) if Alluxio isn't deployed. */
function StorageTiersBand() {
  const { state } = useLiveResource<AlluxioStats>(() => frame.cluster.alluxio())
  if (state.phase !== 'ready') return null
  const a = state.data

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <Stack className="text-primary" />
          Alluxio Storage Tiers
          <Badge variant="outline" className="ml-auto font-mono text-accent border-current gap-1">
            <Lightning size={12} />
            {a.cacheHitRate.toFixed(0)}% cache hit
          </Badge>
        </CardTitle>
      </CardHeader>
      <CardContent className={`grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4`}>
        {a.tiers.map((t) => {
          const pct = t.totalBytes ? (t.usedBytes / t.totalBytes) * 100 : 0
          return (
            <div key={t.name} className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">
                {t.name} tier
              </div>
              <div className="font-mono text-2xl font-bold text-foreground">
                {(t.totalBytes / GiB).toFixed(1)} GiB
              </div>
              <Progress value={pct} className="h-1" />
              <div className="text-[10px] text-muted-foreground font-mono">
                {(t.usedBytes / GiB).toFixed(1)} GiB used · {TIER_DETAIL[t.name] ?? ''}
              </div>
            </div>
          )
        })}
      </CardContent>
    </Card>
  )
}

const phaseTone = (p: string) =>
  p === 'Running' ? 'text-accent' : p === 'Pending' ? 'text-warning' : 'text-destructive'

export function WorkloadPlacementView() {
  const { state, reload } = useLiveResource<NodePlacement[]>(() => frame.cluster.placement())
  const nodes = state.phase === 'ready' ? state.data : []
  const totalPods = nodes.reduce((s, n) => s + n.total, 0)

  return (
    <div className="space-y-6">
      <StorageTiersBand />

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrives className="text-primary" />
            Workload Placement
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
            Where compute actually sits — {totalPods} workload pods grouped by the node scheduling
            them (system namespaces excluded).
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No workload pods scheduled." />

      {state.phase === 'ready' &&
        nodes.map((n) => (
          <Card key={n.node}>
            <CardHeader>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <CardTitle className="font-mono text-lg">{n.node}</CardTitle>
                <Badge variant="outline" className="font-mono text-accent border-current">
                  {n.running}/{n.total} running
                </Badge>
              </div>
            </CardHeader>
            <CardContent>
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
                {n.pods.map((p) => (
                  <div
                    key={`${p.namespace}/${p.name}`}
                    className="p-2 rounded border border-border/50 bg-secondary/20 flex items-center justify-between gap-2"
                  >
                    <div className="min-w-0">
                      <div className="font-mono text-xs truncate">{p.name}</div>
                      <div className="text-[10px] text-muted-foreground font-mono">
                        {p.app ?? p.namespace}
                      </div>
                    </div>
                    <span className={`text-[10px] font-mono shrink-0 ${phaseTone(p.phase)}`}>
                      {p.phase}
                    </span>
                  </div>
                ))}
              </div>
            </CardContent>
          </Card>
        ))}
    </div>
  )
}
