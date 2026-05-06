import { BurstBufferClusterStats, BurstBufferNodeStats, BurstBufferState } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { HardDrives, ArrowDown, ArrowUp } from '@phosphor-icons/react'

// ── Simulated state ───────────────────────────────────────────────────────────

const MOCK_NODES: BurstBufferNodeStats[] = [
  { nodeId: 'gpu-0', nodeName: 'gpu-node-00', state: 'absorbing', usedGB: 180, totalGB: 400, writeRateMBps: 3200, drainRateMBps: 450,  alluxioWorkerGB: 160, stagedCheckpoints: 8  },
  { nodeId: 'gpu-1', nodeName: 'gpu-node-01', state: 'absorbing', usedGB: 220, totalGB: 400, writeRateMBps: 2800, drainRateMBps: 380,  alluxioWorkerGB: 195, stagedCheckpoints: 11 },
  { nodeId: 'gpu-2', nodeName: 'gpu-node-02', state: 'draining',  usedGB: 310, totalGB: 400, writeRateMBps: 120,  drainRateMBps: 890,  alluxioWorkerGB: 280, stagedCheckpoints: 15 },
  { nodeId: 'gpu-3', nodeName: 'gpu-node-03', state: 'full',      usedGB: 398, totalGB: 400, writeRateMBps: 0,    drainRateMBps: 1200, alluxioWorkerGB: 370, stagedCheckpoints: 21 },
  { nodeId: 'gpu-4', nodeName: 'gpu-node-04', state: 'idle',      usedGB: 12,  totalGB: 400, writeRateMBps: 0,    drainRateMBps: 0,    alluxioWorkerGB: 10,  stagedCheckpoints: 0  },
  { nodeId: 'gpu-5', nodeName: 'gpu-node-05', state: 'absorbing', usedGB: 95,  totalGB: 400, writeRateMBps: 1800, drainRateMBps: 210,  alluxioWorkerGB: 82,  stagedCheckpoints: 4  },
]

const CLUSTER_STATS: BurstBufferClusterStats = {
  nodes: MOCK_NODES,
  totalWriteRateMBps: MOCK_NODES.reduce((s, n) => s + n.writeRateMBps, 0),
  totalDrainRateMBps: MOCK_NODES.reduce((s, n) => s + n.drainRateMBps, 0),
  totalUsedGB: MOCK_NODES.reduce((s, n) => s + n.usedGB, 0),
  totalCapacityGB: MOCK_NODES.reduce((s, n) => s + n.totalGB, 0),
}

// ── Helpers ───────────────────────────────────────────────────────────────────

const STATE_CONFIG: Record<BurstBufferState, { label: string; badge: string }> = {
  idle:      { label: 'Idle',      badge: 'bg-secondary text-muted-foreground border-border' },
  absorbing: { label: 'Absorbing', badge: 'bg-primary/20 text-primary border-primary/30' },
  draining:  { label: 'Draining',  badge: 'bg-accent/20 text-accent border-accent/30' },
  full:      { label: 'Full',      badge: 'bg-destructive/20 text-destructive border-destructive/30' },
}

function capColor(pct: number) {
  if (pct >= 90) return 'text-destructive'
  if (pct >= 70) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-foreground'
}

// ── Node tile ─────────────────────────────────────────────────────────────────

function BBNodeTile({ node }: { node: BurstBufferNodeStats }) {
  const capPct  = (node.usedGB / node.totalGB) * 100
  const cfg     = STATE_CONFIG[node.state]

  return (
    <div className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2">
      <div className="flex items-center justify-between">
        <span className="font-mono text-xs font-bold">{node.nodeName}</span>
        <Badge className={`text-[10px] border ${cfg.badge}`}>{cfg.label}</Badge>
      </div>

      <div className="space-y-1">
        <div className="flex justify-between text-xs">
          <span className="text-muted-foreground">NVMe used</span>
          <span className={`font-mono font-bold ${capColor(capPct)}`}>{node.usedGB}/{node.totalGB} GB</span>
        </div>
        <Progress value={capPct} className="h-1.5" />
      </div>

      <div className="grid grid-cols-2 gap-1 text-[10px] text-muted-foreground">
        <div>
          <div className="flex items-center gap-0.5">
            <ArrowDown size={9} className="text-primary" />
            <span className="font-mono font-bold text-primary">{node.writeRateMBps.toLocaleString()} MB/s</span>
          </div>
          <div>Absorb</div>
        </div>
        <div>
          <div className="flex items-center gap-0.5">
            <ArrowUp size={9} className="text-accent" />
            <span className="font-mono font-bold text-accent">{node.drainRateMBps.toLocaleString()} MB/s</span>
          </div>
          <div>→ Ceph</div>
        </div>
        <div className="col-span-2">
          <span className="font-mono font-bold text-foreground">{node.stagedCheckpoints}</span> checkpoints staged
        </div>
      </div>
    </div>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function BurstBufferDashboard() {
  const s = CLUSTER_STATS
  const capPct = (s.totalUsedGB / s.totalCapacityGB) * 100
  const fullNodes = s.nodes.filter(n => n.state === 'full').length
  const totalCheckpoints = s.nodes.reduce((sum, n) => sum + n.stagedCheckpoints, 0)

  return (
    <div className="space-y-6">
      {/* Summary */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <HardDrives className="text-primary" />
            Burst Buffer NVMe — Alluxio Write-Behind
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Absorb Rate</div>
              <div className="font-mono text-2xl font-bold text-primary">
                <ArrowDown size={18} className="inline" /> {(s.totalWriteRateMBps / 1024).toFixed(1)} GB/s
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Drain → Ceph</div>
              <div className="font-mono text-2xl font-bold text-accent">
                <ArrowUp size={18} className="inline" /> {(s.totalDrainRateMBps / 1024).toFixed(1)} GB/s
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Used Capacity</div>
              <div className={`font-mono text-2xl font-bold ${capColor(capPct)}`}>
                {s.totalUsedGB} <span className="text-sm text-muted-foreground">/ {s.totalCapacityGB} GB</span>
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Full Nodes</div>
              <div className={`font-mono text-2xl font-bold ${fullNodes > 0 ? 'text-destructive' : 'text-foreground'}`}>{fullNodes}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Staged Checkpoints</div>
              <div className="font-mono text-2xl font-bold text-foreground">{totalCheckpoints}</div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="flex justify-between text-xs text-muted-foreground">
              <span className="uppercase tracking-wide">Cluster NVMe Burst Buffer</span>
              <span className="font-mono">{s.totalUsedGB} / {s.totalCapacityGB} GB ({capPct.toFixed(0)}%)</span>
            </div>
            <Progress value={capPct} className="h-3" />
          </div>
        </CardContent>
      </Card>

      {/* Per-node grid */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Per-Node Burst Buffer</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {s.nodes.map(n => <BBNodeTile key={n.nodeId} node={n} />)}
          </div>
        </CardContent>
      </Card>

      {/* Config reference */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Alluxio Write-Behind Config</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">{`# deploy/caching/burst-buffer-nvme.yaml  (excerpt)
# Alluxio worker: write-behind tiered store
alluxio.worker.ramdisk.size: 0
alluxio.worker.tieredstore.levels: 2
alluxio.worker.tieredstore.level0.alias: SSD
alluxio.worker.tieredstore.level0.dirs.path: /mnt/nvme0n1/alluxio
alluxio.worker.tieredstore.level0.dirs.quota: 400GB
alluxio.worker.tieredstore.level0.watermark.high.ratio: 0.85
alluxio.worker.tieredstore.level0.watermark.low.ratio: 0.70
alluxio.worker.tieredstore.level1.alias: HDD   # maps to Ceph RBD via CSI
alluxio.worker.tieredstore.level1.dirs.path: /mnt/ceph
alluxio.worker.tieredstore.reserver.interval: 1s
# Write-behind: data lands on NVMe first, drained async to Ceph
alluxio.user.file.writetype.default: ASYNC_THROUGH`}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
