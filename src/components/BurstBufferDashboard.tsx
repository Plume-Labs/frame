import { BurstBufferClusterStats, BurstBufferNodeStats, BurstBufferState } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { HardDrives, ArrowDown, ArrowUp } from '@phosphor-icons/react'
import { EntityTile, MicroStats, TileMeter, TuningDashboard } from '@/components/TuningDashboard'
import { inverseScoreTone } from '@/lib/thresholds'

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

/** Capacity used: <=70% healthy, <=90% warning, above that the buffer is about to stall. */
const CAP_GOOD_PCT = 70
const CAP_OK_PCT = 90

// ── Node tile ─────────────────────────────────────────────────────────────────

function BBNodeTile({ node }: { node: BurstBufferNodeStats }) {
  const capPct  = (node.usedGB / node.totalGB) * 100
  const cfg     = STATE_CONFIG[node.state]

  return (
    <EntityTile
      name={node.nodeName}
      badge={<Badge className={`text-[10px] border ${cfg.badge}`}>{cfg.label}</Badge>}
    >
      <TileMeter
        label="NVMe used"
        value={capPct}
        display={`${node.usedGB}/${node.totalGB} GB`}
        tone={inverseScoreTone(capPct, CAP_GOOD_PCT, CAP_OK_PCT)}
      />

      <MicroStats
        items={[
          {
            value: (
              <span className="inline-flex items-center gap-0.5">
                <ArrowDown size={9} />
                {node.writeRateMBps.toLocaleString()} MB/s
              </span>
            ),
            label: 'Absorb',
            tone: 'primary',
          },
          {
            value: (
              <span className="inline-flex items-center gap-0.5">
                <ArrowUp size={9} />
                {node.drainRateMBps.toLocaleString()} MB/s
              </span>
            ),
            label: '→ Ceph',
            tone: 'accent',
          },
        ]}
      />

      <div className="text-[10px] text-muted-foreground">
        <span className="font-mono font-bold text-foreground">{node.stagedCheckpoints}</span> checkpoints staged
      </div>
    </EntityTile>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function BurstBufferDashboard() {
  const s = CLUSTER_STATS
  const capPct = (s.totalUsedGB / s.totalCapacityGB) * 100
  const fullNodes = s.nodes.filter(n => n.state === 'full').length
  const totalCheckpoints = s.nodes.reduce((sum, n) => sum + n.stagedCheckpoints, 0)

  return (
    <TuningDashboard<BurstBufferNodeStats>
      title="Burst Buffer NVMe — Alluxio Write-Behind"
      icon={<HardDrives className="text-primary" />}
      stats={[
        {
          label: 'Absorb Rate',
          value: <><ArrowDown size={18} className="inline" /> {(s.totalWriteRateMBps / 1024).toFixed(1)} GB/s</>,
          tone: 'primary',
        },
        {
          label: 'Drain → Ceph',
          value: <><ArrowUp size={18} className="inline" /> {(s.totalDrainRateMBps / 1024).toFixed(1)} GB/s</>,
          tone: 'accent',
        },
        {
          label: 'Used Capacity',
          value: <>{s.totalUsedGB} <span className="text-sm text-muted-foreground">/ {s.totalCapacityGB} GB</span></>,
          tone: inverseScoreTone(capPct, CAP_GOOD_PCT, CAP_OK_PCT),
        },
        {
          label: 'Full Nodes',
          value: fullNodes,
          tone: fullNodes > 0 ? 'destructive' : 'foreground',
        },
        { label: 'Staged Checkpoints', value: totalCheckpoints, tone: 'foreground' },
      ]}
      progress={{
        value: capPct,
        captionLeft: <span className="uppercase tracking-wide">Cluster NVMe Burst Buffer</span>,
        captionRight: <span className="font-mono">{s.totalUsedGB} / {s.totalCapacityGB} GB ({capPct.toFixed(0)}%)</span>,
      }}
      entitiesTitle="Per-Node Burst Buffer"
      entities={s.nodes}
      entityKey={n => n.nodeId}
      renderEntity={n => <BBNodeTile node={n} />}
      configTitle="Alluxio Write-Behind Config"
      config={`# deploy/caching/burst-buffer-nvme.yaml  (excerpt)
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
alluxio.user.file.writetype.default: ASYNC_THROUGH`}
    />
  )
}
