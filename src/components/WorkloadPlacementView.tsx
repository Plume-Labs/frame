import {
  CacheStats,
  NodePlacement,
  StorageTiers,
  createFrameClient,
} from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { HardDrives, ArrowClockwise, Lightning, Stack } from '@phosphor-icons/react'

const frame = createFrameClient()

function MemoryAndCache() {
  const { state } = useLiveResource<{ tiers: StorageTiers; cache: CacheStats | null }>(
    async () => {
      const tiers = await frame.cluster.tiers()
      let cache: CacheStats | null = null
      try {
        cache = await frame.cluster.cache()
      } catch {
        cache = null
      }
      return { tiers, cache }
    },
  )
  if (state.phase !== 'ready') return null
  const { tiers, cache } = state.data

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <Stack className="text-primary" />
          Memory Tiers &amp; Cache
        </CardTitle>
      </CardHeader>
      <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4">
        <Tile label="RAM tier" value={`${tiers.ramGiB.toFixed(0)} GiB`} />
        <Tile label="NVMe tier (local)" value={`${tiers.nvmeGiB.toFixed(0)} GiB`} />
        <Tile label="Object tier (Ceph)" value={`${tiers.objectGiB.toFixed(0)} GiB`} />
        <div className="space-y-1">
          <div className="text-xs text-muted-foreground uppercase tracking-wide flex items-center gap-1">
            <Lightning size={12} /> Cache hit-rate
          </div>
          {cache && cache.up ? (
            <>
              <div className="font-mono text-2xl font-bold text-accent">
                {cache.hitRate.toFixed(0)}%
              </div>
              <Progress value={cache.hitRate} className="h-1" />
              <div className="text-[10px] text-muted-foreground font-mono">
                {cache.hits.toLocaleString()} hits / {cache.misses.toLocaleString()} miss
              </div>
            </>
          ) : (
            <div className="font-mono text-sm text-muted-foreground">no exporter</div>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function Tile({ label, value }: { label: string; value: string }) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className="font-mono text-2xl font-bold text-foreground">{value}</div>
    </div>
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
      <MemoryAndCache />

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
