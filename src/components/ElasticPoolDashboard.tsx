import type { ReactNode } from 'react'
import { ElasticPool, ElasticPoolState, ServiceClass } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { EntityTile, StatTile, TileMeter, TuningDashboard } from '@/components/TuningDashboard'
import { TONE_TEXT, Tone } from '@/lib/thresholds'
import { VOLCANO_QUEUES } from '@/lib/tuning-fixtures'
import { ChartBar, ArrowUp, ArrowDown, MinusCircle, Warning } from '@phosphor-icons/react'

// ── Simulated pool state ──────────────────────────────────────────────────────
//
// Queue name + DRF weight come from the shared `VOLCANO_QUEUES` fixture, which
// SchedulerDashboard's weight table should also be reading from.

const MOCK_POOLS: ElasticPool[] = [
  {
    name: 'lpar-inference-high',
    volcanoQueue: VOLCANO_QUEUES['neura-high'].name,
    serviceClass: 'HIGH',
    state: 'stable',
    quota: { cpuMin: 64, cpuMax: 256, memoryMinGi: 128, memoryMaxGi: 512, gpuMin: 4, gpuMax: 32 },
    allocatedCPU: 180, allocatedMemoryGi: 360, allocatedGPU: 16,
    podCount: 24, weight: VOLCANO_QUEUES['neura-high'].weight, reclaimable: false,
  },
  {
    name: 'lpar-training-low',
    volcanoQueue: VOLCANO_QUEUES['neura-low'].name,
    serviceClass: 'LOW',
    state: 'expanding',
    quota: { cpuMin: 32, cpuMax: 512, memoryMinGi: 64, memoryMaxGi: 1024, gpuMin: 0, gpuMax: 64 },
    allocatedCPU: 290, allocatedMemoryGi: 580, allocatedGPU: 32,
    podCount: 48, weight: VOLCANO_QUEUES['neura-low'].weight, reclaimable: true,
  },
  {
    name: 'lpar-batch-medium',
    volcanoQueue: VOLCANO_QUEUES['neura-medium'].name,
    serviceClass: 'MEDIUM',
    state: 'contracting',
    quota: { cpuMin: 16, cpuMax: 128, memoryMinGi: 32, memoryMaxGi: 256, gpuMin: 0, gpuMax: 16 },
    allocatedCPU: 62, allocatedMemoryGi: 120, allocatedGPU: 4,
    podCount: 10, weight: VOLCANO_QUEUES['neura-medium'].weight, reclaimable: true,
  },
  {
    name: 'lpar-eval',
    volcanoQueue: VOLCANO_QUEUES['neura-low'].name,
    serviceClass: 'LOW',
    state: 'draining',
    quota: { cpuMin: 0, cpuMax: 64, memoryMinGi: 0, memoryMaxGi: 128, gpuMin: 0, gpuMax: 8 },
    allocatedCPU: 12, allocatedMemoryGi: 24, allocatedGPU: 2,
    // Per-pool override — this pool sits below its queue's default weight.
    podCount: 3, weight: 5, reclaimable: true,
  },
]

// ── Helpers ───────────────────────────────────────────────────────────────────

const STATE_CONFIG: Record<ElasticPoolState, { label: string; badge: string; icon: ReactNode }> = {
  stable:      { label: 'Stable',      badge: 'bg-accent/20 text-accent border-accent/30',            icon: <MinusCircle size={12} /> },
  expanding:   { label: 'Expanding',   badge: 'bg-primary/20 text-primary border-primary/30',         icon: <ArrowUp size={12} /> },
  contracting: { label: 'Contracting', badge: 'bg-warning/10 text-warning border-warning/30',         icon: <ArrowDown size={12} /> },
  draining:    { label: 'Draining',    badge: 'bg-destructive/20 text-destructive border-destructive/30', icon: <Warning size={12} /> },
}

const SC_TONES: Record<ServiceClass, Tone> = {
  HIGH:   'destructive',
  MEDIUM: 'warning',
  LOW:    'accent',
}

function cpuPct(pool: ElasticPool) {
  return pool.quota.cpuMax > 0 ? (pool.allocatedCPU / pool.quota.cpuMax) * 100 : 0
}
function memPct(pool: ElasticPool) {
  return pool.quota.memoryMaxGi > 0 ? (pool.allocatedMemoryGi / pool.quota.memoryMaxGi) * 100 : 0
}
function gpuPct(pool: ElasticPool) {
  return pool.quota.gpuMax > 0 ? (pool.allocatedGPU / pool.quota.gpuMax) * 100 : 0
}

// ── Pool row ──────────────────────────────────────────────────────────────────

function PoolRow({ pool }: { pool: ElasticPool }) {
  const stCfg = STATE_CONFIG[pool.state]

  return (
    <EntityTile
      name={
        <span className="block">
          <span className="block text-sm">{pool.name}</span>
          <span className="block font-normal text-[10px] text-muted-foreground mt-0.5">
            queue: {pool.volcanoQueue} · weight {pool.weight} · {pool.reclaimable ? 'reclaimable' : 'non-reclaimable'}
          </span>
        </span>
      }
      badge={
        <span className="flex items-center gap-2 shrink-0">
          <span className={`font-mono text-xs font-bold ${TONE_TEXT[SC_TONES[pool.serviceClass]]}`}>{pool.serviceClass}</span>
          <Badge className={`text-[10px] border flex items-center gap-1 ${stCfg.badge}`}>
            {stCfg.icon}{stCfg.label}
          </Badge>
        </span>
      }
    >
      <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div className="space-y-1">
          <TileMeter
            label="CPU"
            value={cpuPct(pool)}
            display={`${pool.allocatedCPU} / ${pool.quota.cpuMax} cores`}
            tone="foreground"
          />
          <div className="text-[10px] text-muted-foreground font-mono">min {pool.quota.cpuMin}</div>
        </div>
        <div className="space-y-1">
          <TileMeter
            label="Memory"
            value={memPct(pool)}
            display={`${pool.allocatedMemoryGi} / ${pool.quota.memoryMaxGi} Gi`}
            tone="foreground"
          />
          <div className="text-[10px] text-muted-foreground font-mono">min {pool.quota.memoryMinGi} Gi</div>
        </div>
        <div className="space-y-1">
          <TileMeter
            label="GPU"
            value={gpuPct(pool)}
            display={`${pool.allocatedGPU} / ${pool.quota.gpuMax}`}
            tone="foreground"
          />
          <div className="text-[10px] text-muted-foreground font-mono">min {pool.quota.gpuMin}</div>
        </div>
      </div>

      <div className="text-xs text-muted-foreground">
        Pods: <span className="font-mono text-foreground">{pool.podCount}</span>
      </div>
    </EntityTile>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function ElasticPoolDashboard() {
  const pools = MOCK_POOLS
  const totalCPU       = pools.reduce((s, p) => s + p.allocatedCPU, 0)
  const totalCPUMax    = pools.reduce((s, p) => s + p.quota.cpuMax, 0)
  const totalMemGi     = pools.reduce((s, p) => s + p.allocatedMemoryGi, 0)
  const totalMemMaxGi  = pools.reduce((s, p) => s + p.quota.memoryMaxGi, 0)
  const totalGPU       = pools.reduce((s, p) => s + p.allocatedGPU, 0)
  const totalGPUMax    = pools.reduce((s, p) => s + p.quota.gpuMax, 0)
  const expanding      = pools.filter(p => p.state === 'expanding').length
  const contracting    = pools.filter(p => p.state === 'contracting').length

  const summary: StatTile[] = [
    { label: 'Pools', value: pools.length },
    { label: 'Expanding', value: expanding, tone: expanding > 0 ? 'primary' : 'foreground' },
    { label: 'Contracting', value: contracting, tone: contracting > 0 ? 'warning' : 'foreground' },
    { label: 'GPU Allocated', value: `${totalGPU} / ${totalGPUMax}`, tone: 'accent' },
    { label: 'CPU Allocated', value: `${totalCPU} / ${totalCPUMax}` },
  ]

  return (
    <TuningDashboard
      title="Elastic LPAR Pools — Volcano Queue"
      icon={<ChartBar className="text-primary" />}
      stats={summary}
      progress={{
        value: totalCPUMax > 0 ? (totalCPU / totalCPUMax) * 100 : 0,
        captionLeft: <span className="uppercase tracking-wide">Cluster CPU</span>,
        captionRight: <span className="font-mono">{totalCPU} / {totalCPUMax} cores</span>,
      }}
      /* Second full-width bar — the template's `progress` slot only carries one. */
      note={
        <div className="space-y-1">
          <Progress value={totalMemMaxGi > 0 ? (totalMemGi / totalMemMaxGi) * 100 : 0} className="h-3" />
          <div className="flex justify-between text-xs text-muted-foreground">
            <span className="uppercase tracking-wide">Cluster Memory</span>
            <span className="font-mono">{totalMemGi} / {totalMemMaxGi} Gi</span>
          </div>
        </div>
      }
      entitiesTitle="Pool Details"
      entities={pools}
      entityKey={(p) => p.name}
      renderEntity={(p) => <PoolRow pool={p} />}
      entityLayout="stack"
      configTitle="GitOps Queue Patch Reference"
      config={`# Source of truth:
# deploy/kubernetes/scheduling/elastic-lpar-pools.yaml
#
# Apply/reconcile:
kubectl apply -f deploy/kubernetes/scheduling/elastic-lpar-pools.yaml
#
# Runtime expand/contract patch example:
kubectl patch queue lpar-training-low \\
  --type=merge \\
  -p '{"spec":{"capability":{"cpu":"384"}}}'`}
    />
  )
}
