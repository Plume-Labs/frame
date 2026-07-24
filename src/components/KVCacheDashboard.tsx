import { KVCacheClusterStats, KVCacheNodeStats, VLLMKVTransferBackend } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { EntityTile, MicroStats, StatTile, TileMeter, TuningDashboard } from '@/components/TuningDashboard'
import { Tone, inverseScoreTone, scoreTone } from '@/lib/thresholds'
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

/** Hit rates: higher is better — accent ≥ 80%, warning ≥ 60%. */
const hitTone = (rate: number): Tone => scoreTone(rate, 0.8, 0.6)

/**
 * RDMA bandwidth is a saturation signal, not a score: it means cache misses
 * served from a *peer*, so lower is better. Below the 30 GB/s band there is
 * nothing to celebrate either, so the good band renders neutral.
 */
function bwTone(gbps: number): Tone {
  const tone = inverseScoreTone(gbps, 30, 50)
  return tone === 'accent' ? 'foreground' : tone
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
    <EntityTile
      name={node.nodeName}
      badge={
        <span className="font-mono text-[10px] text-muted-foreground shrink-0">
          {node.activeQueuePairs} QPs
        </span>
      }
    >
      <TileMeter label="Local hit" value={node.localHitRate * 100} tone={hitTone(node.localHitRate)} />
      <TileMeter label="RDMA hit" value={node.rdmaHitRate * 100} tone="primary" />
      <TileMeter
        label="KV capacity"
        value={capPct}
        display={`${node.usedCapacityGB}/${node.totalCapacityGB} GB`}
        tone="foreground"
      />

      <MicroStats
        items={[
          { label: 'Combined', value: `${(totalHit * 100).toFixed(0)}%`, tone: hitTone(totalHit) },
          {
            label: 'RDMA BW',
            value: <>{node.rdmaBandwidthGBps.toFixed(0)}<span className="text-muted-foreground"> GB/s</span></>,
            tone: bwTone(node.rdmaBandwidthGBps),
          },
          {
            label: 'Latency',
            value: <>{node.rdmaLatencyUs.toFixed(1)}<span className="text-muted-foreground"> µs</span></>,
          },
        ]}
      />
    </EntityTile>
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

  const summary: StatTile[] = [
    { label: 'Avg Local Hit', value: `${(avgLocal * 100).toFixed(0)}%`, tone: hitTone(avgLocal) },
    { label: 'Avg RDMA Hit', value: `${(avgRDMA * 100).toFixed(0)}%`, tone: 'primary' },
    {
      label: 'Total RDMA BW',
      value: (
        <span className="flex items-center gap-1">
          <ArrowsLeftRight size={18} className="text-primary" />{totalBW.toFixed(0)} <span className="text-sm text-muted-foreground">GB/s</span>
        </span>
      ),
    },
    { label: 'Active QPs', value: totalQPs, tone: 'accent' },
    {
      label: 'Prefill Savings',
      value: <><Lightning size={18} className="inline text-accent" /> {stats.prefillSavingsPct}%</>,
      tone: 'accent',
    },
  ]

  return (
    <TuningDashboard
      title="Distributed KV-Cache — RDMA"
      icon={<Database className="text-primary" />}
      badge={
        <Badge className="text-xs border bg-primary/20 text-primary border-primary/30">
          {BACKEND_LABELS[stats.backend]}
        </Badge>
      }
      stats={summary}
      progress={{
        value: (usedCap / totalCap) * 100,
        captionLeft: <span className="uppercase tracking-wide">Cluster KV Capacity</span>,
        captionRight: <span className="font-mono">{usedCap} / {totalCap} GB</span>,
      }}
      note={
        <div className="flex gap-4 text-xs text-muted-foreground">
          <span>Evictions/min: <span className="font-mono text-foreground">{stats.evictionsPerMinute}</span></span>
          <span>Nodes: <span className="font-mono text-foreground">{nodes.length}</span></span>
        </div>
      }
      entitiesTitle="Per-Node KV-Cache Stats"
      entities={nodes}
      entityKey={(n) => n.nodeId}
      renderEntity={(n) => <KVNodeTile node={n} />}
      configTitle="vLLM KV-Transfer Config"
      config={`# deploy/caching/vllm-rdma-kvcache.yaml  (excerpt)
kv_transfer_config:
  kv_connector: NixlConnector
  kv_role: kv_both          # each node is producer + consumer
  kv_rank: <NODE_RANK>
  kv_parallel_size: ${nodes.length}
  kv_buffer_device: cuda
  kv_buffer_size: 80        # GB — full VRAM tier for H100
  rdma_backend: roce_v2     # or infiniband
  rdma_port: 18515
  rdma_device: mlx5_0`}
    />
  )
}
