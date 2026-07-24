import { AlluxioStats, CephStatus, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Database, ArrowClockwise, HardDrives, Cube, Stack, Lightning } from '@phosphor-icons/react'

const frame = createFrameClient()

const GiB = 1024 ** 3

const TIER_DETAIL: Record<string, string> = {
  MEM: 'in-memory (fastest)',
  SSD: 'local NVMe/SSD',
  HDD: 'under-store (object/HDD)',
}

function healthTone(h: string): string {
  if (h === 'HEALTH_OK') return 'text-accent'
  if (h === 'HEALTH_WARN') return 'text-warning'
  return 'text-destructive'
}

/** Alluxio stacked cache tiers (MEM -> SSD -> HDD) + cache hit-rate. Silent if absent. */
function AlluxioTiersCard() {
  const { state } = useLiveResource<AlluxioStats>(() => frame.cluster.alluxio())
  if (state.phase !== 'ready') return null
  const a = state.data

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg flex items-center gap-2">
          <Stack className="text-primary" />
          Alluxio Cache Tiers
          <Badge variant="outline" className="ml-auto font-mono text-accent border-current gap-1">
            <Lightning size={12} />
            {a.cacheHitRate.toFixed(0)}% cache hit
          </Badge>
        </CardTitle>
      </CardHeader>
      <CardContent className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
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

export function ClusterStorageView() {
  const { state, reload } = useLiveResource<CephStatus>(() => frame.cluster.ceph())
  const c = state.phase === 'ready' ? state.data : null
  const usedPct = c && c.bytesTotal ? (c.bytesUsed / c.bytesTotal) * 100 : 0

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Database className="text-primary" />
            Ceph Storage
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
        {c && (
          <CardContent className="space-y-4">
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
              <Stat label="Health">
                <span className={`font-mono text-2xl font-bold ${healthTone(c.health)}`}>
                  {c.health.replace('HEALTH_', '')}
                </span>
              </Stat>
              <Stat label="OSDs">
                <span className="font-mono text-2xl font-bold text-foreground">{c.osds}</span>
              </Stat>
              <Stat label="Monitors">
                <span className="font-mono text-2xl font-bold text-foreground">{c.mons}</span>
              </Stat>
              <Stat label="Ceph version">
                <span className="font-mono text-sm font-bold text-muted-foreground">
                  {c.version || '—'}
                </span>
              </Stat>
            </div>

            <div className="space-y-1">
              <Progress value={usedPct} className="h-3" />
              <div className="flex justify-between text-xs text-muted-foreground font-mono">
                <span>{(c.bytesUsed / GiB).toFixed(1)} GiB used</span>
                <span>
                  {(c.bytesAvailable / GiB).toFixed(0)} GiB free of{' '}
                  {(c.bytesTotal / GiB).toFixed(0)} GiB raw
                </span>
              </div>
            </div>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="Rook-Ceph is not installed on this cluster." />

      {c && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg flex items-center gap-2">
              <HardDrives className="text-primary" />
              Pools
            </CardTitle>
          </CardHeader>
          <CardContent>
            {c.pools.length === 0 ? (
              <p className="text-sm text-muted-foreground font-mono">No block pools defined.</p>
            ) : (
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
                {c.pools.map((p) => (
                  <div
                    key={p.name}
                    className="p-3 rounded-lg border border-border bg-secondary/30 flex items-center justify-between"
                  >
                    <span className="font-mono text-xs font-bold flex items-center gap-2">
                      <Cube className="text-primary" size={14} />
                      {p.name}
                    </span>
                    <Badge variant="outline" className="font-mono text-[10px]">
                      ×{p.replication}
                    </Badge>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}

      <AlluxioTiersCard />
    </div>
  )
}

function Stat({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      {children}
    </div>
  )
}
