import { KVCacheClusterStats, KVCacheNodeStats, VLLMKVTransferBackend } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Badge } from '@/components/ui/badge'
import { Database, Lightning, ArrowsLeftRight } from '@phosphor-icons/react'

// ── Simulated cluster state ───────────────────────────────────────────────────

const BACKEND: VLLMKVTransferBackend = 'nixl'

const MOCK_NODES: KVCacheNodeStats[] = [
  { nodeId: 'gpu-0', nodeName: 'gpu-node-00', localHitRate: 0.82, rdmaHitRate: 0.11, rdmaBandwidthGBps: 38.4, usedCapacityGB: 54, totalCapacityGB: 80, activeQueuePairs: 6, rdmaLatencyUs: 1.8 },
  { nodeId: 'gpu-1', nodeName: 'gpu-node-01', localHitRate: 0.74, rdmaHitRate: 0.18, rdmaBandwidthGBps: 52.1, usedCapacityGB: 71, totalCapacityGB: 80, activeQueuePairs: 8, rdmaLatencyUs: 2.1 },
  { nodeId: 'gpu-2', nodeName: 'gpu-node-02', localHitRate: 0.88, rdmaHitRate: 0.07, rdmaBandwidthGBps: 19.6, usedCapacityGB: 42, totalCapacityGB: 80, activeQueuePairs: 4, rdmaLatencyUs: 1.6 },
  { nodeId: 'gpu-3', nodeName: 'gpu-node-03', localHitRate: 0.69, rdmaHitRate: 0.22, rdmaBandwidthGBps: 61.8, usedCapacityGB: 76, totalCapacityGB: 80, activeQueuePairs: 10, rdmaLatencyUs: 2.4 },
  { nodeId: 'gpu-4', nodeName: 'gpu-node-04', localHitRate: 0.91, rdmaHitRate: 0.05, rdmaBandwidthGBps: 12.3, usedCapacityGB: 38, totalCapacityGB: 80, activeQueuePairs: 2, rdmaLatencyUs: 1.4 },
  { nodeId: 'gpu-5', nodeName: 'gpu-node-05', localHitRate: 0.77, rdmaHitRate: 0.15, rdmaBandwidthGBps: 44.7, usedCapacityGB: 65, totalCapacityGB: 80, activeQueuePairs: 7, rdmaLatencyUs: 2.0 },
]

const CLUSTER_STATS: KVCacheClusterStats = {
  backend: BACKEND,
  nodes: MOCK_NODES,
  evictionsPerMinute: 214,
  prefillSavingsPct: 43,
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function hitColor(rate: number) {
  if (rate >= 0.8) return 'text-accent'
  if (rate >= 0.6) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-destructive'
}

function bwColor(gbps: number) {
  if (gbps >= 50) return 'text-destructive'
  if (gbps >= 30) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-foreground'
}

const BACKEND_LABELS: Record<VLLMKVTransferBackend, string> = {
  mooncake: 'Mooncake',
  lmcache: 'LMCache',
  nixl: 'NIXL (vLLM v0.9)',
}

// ── Node tile ─────────────────────────────────────────────────────────────────

function KVNodeTile({ node }: { node: KVCacheNodeStats }) {
  const capPct = (node.usedCapacityGB / node.totalCapacityGB) * 100
  const totalHit = node.localHitRate + node.rdmaHitRate

  return (
    <div className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2">
      <div className="flex items-center justify-between">
        <span className="font-mono text-xs font-bold">{node.nodeName}</span>
        <span className="font-mono text-[10px] text-muted-foreground">{node.activeQueuePairs} QPs</span>
      </div>

      <div>
        <div className="flex justify-between text-xs mb-0.5">
          <span className="text-muted-foreground">Local hit</span>
          <span className={`font-mono font-bold ${hitColor(node.localHitRate)}`}>{(node.localHitRate * 100).toFixed(0)}%</span>
        </div>
        <Progress value={node.localHitRate * 100} className="h-1" />
      </div>

      <div>
        <div className="flex justify-between text-xs mb-0.5">
          <span className="text-muted-foreground">RDMA hit</span>
          <span className="font-mono text-primary">{(node.rdmaHitRate * 100).toFixed(0)}%</span>
        </div>
        <Progress value={node.rdmaHitRate * 100} className="h-1" />
      </div>

      <div>
        <div className="flex justify-between text-xs mb-0.5">
          <span className="text-muted-foreground">KV capacity</span>
          <span className="font-mono text-xs">{node.usedCapacityGB}/{node.totalCapacityGB} GB</span>
        </div>
        <Progress value={capPct} className="h-1" />
      </div>

      <div className="grid grid-cols-3 gap-1 text-[10px] text-muted-foreground pt-1">
        <div>
          <span className={`font-mono font-bold ${hitColor(totalHit)}`}>{(totalHit * 100).toFixed(0)}%</span>
          <div>Combined</div>
        </div>
        <div>
          <span className={`font-mono font-bold ${bwColor(node.rdmaBandwidthGBps)}`}>{node.rdmaBandwidthGBps.toFixed(0)}<span className="text-muted-foreground"> GB/s</span></span>
          <div>RDMA BW</div>
        </div>
        <div>
          <span className="font-mono font-bold text-foreground">{node.rdmaLatencyUs.toFixed(1)}<span className="text-muted-foreground"> µs</span></span>
          <div>Latency</div>
        </div>
      </div>
    </div>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function KVCacheDashboard() {
  const stats = CLUSTER_STATS
  const nodes = stats.nodes

  const avgLocal   = nodes.reduce((s, n) => s + n.localHitRate, 0) / nodes.length
  const avgRDMA    = nodes.reduce((s, n) => s + n.rdmaHitRate, 0) / nodes.length
  const totalBW    = nodes.reduce((s, n) => s + n.rdmaBandwidthGBps, 0)
  const totalQPs   = nodes.reduce((s, n) => s + n.activeQueuePairs, 0)
  const usedCap    = nodes.reduce((s, n) => s + n.usedCapacityGB, 0)
  const totalCap   = nodes.reduce((s, n) => s + n.totalCapacityGB, 0)

  return (
    <div className="space-y-6">
      {/* Summary card */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Database className="text-primary" />
            Distributed KV-Cache — RDMA
            <Badge className="ml-2 text-xs border bg-primary/20 text-primary border-primary/30">
              {BACKEND_LABELS[stats.backend]}
            </Badge>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Avg Local Hit</div>
              <div className={`font-mono text-2xl font-bold ${hitColor(avgLocal)}`}>{(avgLocal * 100).toFixed(0)}%</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Avg RDMA Hit</div>
              <div className="font-mono text-2xl font-bold text-primary">{(avgRDMA * 100).toFixed(0)}%</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Total RDMA BW</div>
              <div className="font-mono text-2xl font-bold text-foreground flex items-center gap-1">
                <ArrowsLeftRight size={18} className="text-primary" />{totalBW.toFixed(0)} <span className="text-sm text-muted-foreground">GB/s</span>
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Active QPs</div>
              <div className="font-mono text-2xl font-bold text-accent">{totalQPs}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Prefill Savings</div>
              <div className="font-mono text-2xl font-bold text-accent">
                <Lightning size={18} className="inline text-accent" /> {stats.prefillSavingsPct}%
              </div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="flex justify-between text-xs text-muted-foreground">
              <span className="uppercase tracking-wide">Cluster KV Capacity</span>
              <span className="font-mono">{usedCap} / {totalCap} GB</span>
            </div>
            <Progress value={(usedCap / totalCap) * 100} className="h-3" />
          </div>

          <div className="flex gap-4 text-xs text-muted-foreground">
            <span>Evictions/min: <span className="font-mono text-foreground">{stats.evictionsPerMinute}</span></span>
            <span>Nodes: <span className="font-mono text-foreground">{nodes.length}</span></span>
          </div>
        </CardContent>
      </Card>

      {/* Per-node heatmap */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Per-Node KV-Cache Stats</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {nodes.map(n => <KVNodeTile key={n.nodeId} node={n} />)}
          </div>
        </CardContent>
      </Card>

      {/* vLLM config reference */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">vLLM KV-Transfer Config</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">{`# deploy/caching/vllm-rdma-kvcache.yaml  (excerpt)
kv_transfer_config:
  kv_connector: NixlConnector
  kv_role: kv_both          # each node is producer + consumer
  kv_rank: <NODE_RANK>
  kv_parallel_size: ${nodes.length}
  kv_buffer_device: cuda
  kv_buffer_size: 80        # GB — full VRAM tier for H100
  rdma_backend: roce_v2     # or infiniband
  rdma_port: 18515
  rdma_device: mlx5_0`}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
