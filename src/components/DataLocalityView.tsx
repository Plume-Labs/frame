import { ClusterNode } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { HardDrives, Database, Cpu, Lightning } from '@phosphor-icons/react'

interface DataLocalityViewProps {
  nodes: ClusterNode[]
}

const TIER_COLORS: Record<string, { bg: string; text: string; label: string }> = {
  ram:    { bg: 'bg-accent',                       text: 'text-accent',                       label: 'RAM' },
  nvme:   { bg: 'bg-primary',                      text: 'text-primary',                      label: 'NVMe' },
  object: { bg: 'bg-warning',       text: 'text-warning',       label: 'Object' },
}

const TIER_ORDER = ['ram', 'nvme', 'object'] as const

function CacheHeatCell({ value }: { value: number }) {
  let color = 'bg-accent'
  if (value < 0.5) color = 'bg-warning'
  if (value < 0.3) color = 'bg-destructive'
  return (
    <div
      title={`Cache hit rate: ${(value * 100).toFixed(1)}%`}
      className={`w-full h-6 rounded ${color} transition-opacity`}
      style={{ opacity: Math.max(0.15, value) }}
    />
  )
}

export function DataLocalityView({ nodes }: DataLocalityViewProps) {
  const activeNodes = nodes.filter(n => n.status !== 'offline')

  // Tier distribution
  const tierCounts = { ram: 0, nvme: 0, object: 0 }
  activeNodes.forEach(n => {
    const tier = n.hardware.storageTier
    if (tier in tierCounts) tierCounts[tier as keyof typeof tierCounts]++
  })
  const total = activeNodes.length || 1

  // NUMA distribution
  const numaCounts: Record<number, number> = {}
  activeNodes.forEach(n => {
    const numa = n.hardware.numaNode
    numaCounts[numa] = (numaCounts[numa] || 0) + 1
  })

  const avgCacheHit = activeNodes.length > 0
    ? activeNodes.reduce((s, n) => s + n.hardware.cacheHitRate, 0) / activeNodes.length
    : 0

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrives className="text-primary" />
            Data Locality — Cache Heatmap
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-4 mb-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Avg Cache Hit Rate</div>
              <div className={`font-mono text-2xl font-bold ${avgCacheHit > 0.7 ? 'text-accent' : 'text-warning'}`}>
                {(avgCacheHit * 100).toFixed(1)}%
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Cache Tiers</div>
              <div className="flex gap-2">
                {TIER_ORDER.map(tier => {
                  const c = TIER_COLORS[tier]
                  return (
                    <span key={tier} className={`font-mono text-xs font-bold ${c.text}`}>
                      {c.label}: {tierCounts[tier as keyof typeof tierCounts]}
                    </span>
                  )
                })}
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">NUMA Nodes</div>
              <div className="flex gap-2">
                {Object.entries(numaCounts).map(([numa, cnt]) => (
                  <span key={numa} className="font-mono text-xs">
                    NUMA{numa}: <span className="font-bold text-primary">{cnt}</span>
                  </span>
                ))}
              </div>
            </div>
          </div>

          {/* Per-node heatmap grid */}
          <div className="space-y-2">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">Per-node Cache Hit Rate</div>
            <div className="grid gap-1" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(60px, 1fr))' }}>
              {activeNodes.map(node => (
                <div key={node.id} className="space-y-1">
                  <CacheHeatCell value={node.hardware.cacheHitRate} />
                  <div className="text-[10px] font-mono text-center text-muted-foreground truncate">{node.name}</div>
                </div>
              ))}
            </div>
            <div className="flex items-center gap-3 pt-1">
              <div className="text-xs text-muted-foreground">Low</div>
              <div className="flex gap-1">
                {[0.1, 0.3, 0.5, 0.7, 0.9].map(v => (
                  <div key={v} className="w-6 h-3 rounded bg-accent" style={{ opacity: v }} />
                ))}
              </div>
              <div className="text-xs text-muted-foreground">High</div>
            </div>
          </div>
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 sm:grid-cols-2 gap-6">
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg flex items-center gap-2">
              <Database className="text-primary" />
              Storage Tier Distribution
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            {TIER_ORDER.map(tier => {
              const c = TIER_COLORS[tier]
              const count = tierCounts[tier as keyof typeof tierCounts]
              return (
                <div key={tier} className="space-y-1">
                  <div className="flex items-center justify-between text-sm">
                    <span className={`font-mono font-bold ${c.text}`}>{c.label}</span>
                    <span className="font-mono">{count} nodes ({((count / total) * 100).toFixed(0)}%)</span>
                  </div>
                  <div className="w-full h-2 rounded-full bg-secondary">
                    <div className={`h-2 rounded-full ${c.bg}`} style={{ width: `${(count / total) * 100}%` }} />
                  </div>
                </div>
              )
            })}
            <div className="pt-2 text-xs text-muted-foreground">
              Tiering strategy: RAM → NVMe → Object Storage (Alluxio)
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg flex items-center gap-2">
              <Cpu className="text-primary" />
              NUMA Topology
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            {Object.entries(numaCounts).map(([numa, cnt]) => (
              <div key={numa} className="space-y-2">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-sm font-bold">NUMA Node {numa}</span>
                  <span className="font-mono text-sm">{cnt} nodes</span>
                </div>
                <div className="space-y-1">
                  {activeNodes
                    .filter(n => n.hardware.numaNode === Number(numa))
                    .slice(0, 6)
                    .map(n => (
                      <div key={n.id} className="flex items-center justify-between p-2 rounded bg-secondary/40 text-xs">
                        <span className="font-mono">{n.name}</span>
                        <span className={`font-mono ${n.hardware.cacheHitRate > 0.7 ? 'text-accent' : 'text-warning'}`}>
                          {(n.hardware.cacheHitRate * 100).toFixed(0)}% hit
                        </span>
                      </div>
                    ))}
                  {activeNodes.filter(n => n.hardware.numaNode === Number(numa)).length > 6 && (
                    <div className="text-xs text-muted-foreground text-center">
                      +{activeNodes.filter(n => n.hardware.numaNode === Number(numa)).length - 6} more
                    </div>
                  )}
                </div>
              </div>
            ))}
            <div className="pt-2 text-xs text-muted-foreground">
              Policy: single-numa-node topology manager
            </div>
          </CardContent>
        </Card>
      </div>

      {/* ── NUMA-aware placement + 1 GB Huge Pages ───────────────────────────── */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <Lightning className="text-primary" />
            NUMA-aware Placement + 1 GB Huge Pages
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Hugepages Policy</div>
              <div className="font-mono text-sm font-bold text-primary">1 GB (static)</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Total Hugepages</div>
              <div className="font-mono text-sm font-bold text-accent">
                {activeNodes.reduce((s, n) => s + n.hardware.hugepagesGB, 0).toFixed(0)} GB
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">CPU-pinned Cores</div>
              <div className="font-mono text-sm font-bold text-foreground">
                {activeNodes.reduce((s, n) => s + n.hardware.cpuPinnedCores, 0)}
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Topology Policy</div>
              <div className="font-mono text-sm font-bold text-foreground">single-numa-node</div>
            </div>
          </div>

          {/* Per-NUMA hugepages */}
          <div className="space-y-2">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">1 GB Hugepages by NUMA Node</div>
            {Object.entries(numaCounts).map(([numa, cnt]) => {
              const numaNodes = activeNodes.filter(n => n.hardware.numaNode === Number(numa))
              const numaHP    = numaNodes.reduce((s, n) => s + n.hardware.hugepagesGB, 0)
              const numaPinned = numaNodes.reduce((s, n) => s + n.hardware.cpuPinnedCores, 0)
              return (
                <div key={numa} className="p-3 rounded-lg bg-secondary/30 border border-border">
                  <div className="flex items-center justify-between mb-1 text-sm">
                    <span className="font-mono font-bold">NUMA {numa}</span>
                    <div className="flex gap-4 text-xs font-mono">
                      <span className="text-primary">{numaHP.toFixed(0)} GB hugepages</span>
                      <span className="text-muted-foreground">{numaPinned} pinned cores</span>
                      <span className="text-muted-foreground">{cnt} nodes</span>
                    </div>
                  </div>
                  <div className="flex flex-wrap gap-1 mt-1">
                    {numaNodes.slice(0, 8).map(n => (
                      <div key={n.id} className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-secondary border border-border">
                        {n.name} · {n.hardware.hugepagesGB}GB HP · {n.hardware.cpuPinnedCores}c
                      </div>
                    ))}
                    {numaNodes.length > 8 && (
                      <div className="text-[10px] text-muted-foreground px-1.5 py-0.5">+{numaNodes.length - 8} more</div>
                    )}
                  </div>
                </div>
              )
            })}
          </div>

          <div className="text-xs text-muted-foreground">
            1 GB huge pages pre-allocated at boot for model weight tensors in CPU RAM. Combined with CPU pinning and single-numa-node topology manager to guarantee zero-NUMA-crossing latency for GPU↔CPU transfers.
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
