import type { ReactNode } from 'react'
import { ElasticPool, ElasticPoolState } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { ChartBar, ArrowUp, ArrowDown, MinusCircle, Warning } from '@phosphor-icons/react'

// ── Simulated pool state ──────────────────────────────────────────────────────

const MOCK_POOLS: ElasticPool[] = [
  {
    name: 'lpar-inference-high',
    volcanoQueue: 'neura-high',
    serviceClass: 'HIGH',
    state: 'stable',
    quota: { cpuMin: 64, cpuMax: 256, memoryMinGi: 128, memoryMaxGi: 512, gpuMin: 4, gpuMax: 32 },
    allocatedCPU: 180, allocatedMemoryGi: 360, allocatedGPU: 16,
    podCount: 24, weight: 100, reclaimable: false,
  },
  {
    name: 'lpar-training-low',
    volcanoQueue: 'neura-low',
    serviceClass: 'LOW',
    state: 'expanding',
    quota: { cpuMin: 32, cpuMax: 512, memoryMinGi: 64, memoryMaxGi: 1024, gpuMin: 0, gpuMax: 64 },
    allocatedCPU: 290, allocatedMemoryGi: 580, allocatedGPU: 32,
    podCount: 48, weight: 10, reclaimable: true,
  },
  {
    name: 'lpar-batch-medium',
    volcanoQueue: 'neura-medium',
    serviceClass: 'MEDIUM',
    state: 'contracting',
    quota: { cpuMin: 16, cpuMax: 128, memoryMinGi: 32, memoryMaxGi: 256, gpuMin: 0, gpuMax: 16 },
    allocatedCPU: 62, allocatedMemoryGi: 120, allocatedGPU: 4,
    podCount: 10, weight: 50, reclaimable: true,
  },
  {
    name: 'lpar-eval',
    volcanoQueue: 'neura-low',
    serviceClass: 'LOW',
    state: 'draining',
    quota: { cpuMin: 0, cpuMax: 64, memoryMinGi: 0, memoryMaxGi: 128, gpuMin: 0, gpuMax: 8 },
    allocatedCPU: 12, allocatedMemoryGi: 24, allocatedGPU: 2,
    podCount: 3, weight: 5, reclaimable: true,
  },
]

// ── Helpers ───────────────────────────────────────────────────────────────────

const STATE_CONFIG: Record<ElasticPoolState, { label: string; badge: string; icon: ReactNode }> = {
  stable:      { label: 'Stable',      badge: 'bg-accent/20 text-accent border-accent/30',                                                   icon: <MinusCircle size={12} /> },
  expanding:   { label: 'Expanding',   badge: 'bg-primary/20 text-primary border-primary/30',                                               icon: <ArrowUp size={12} /> },
  contracting: { label: 'Contracting', badge: 'bg-[oklch(0.75_0.18_75)]/10 text-[oklch(0.75_0.18_75)] border-[oklch(0.75_0.18_75)]/30',   icon: <ArrowDown size={12} /> },
  draining:    { label: 'Draining',    badge: 'bg-destructive/20 text-destructive border-destructive/30',                                    icon: <Warning size={12} /> },
}

const SC_COLORS: Record<string, string> = {
  HIGH:   'text-destructive',
  MEDIUM: 'text-[oklch(0.75_0.18_75)]',
  LOW:    'text-accent',
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
    <div className="p-4 rounded-lg bg-secondary/30 border border-border space-y-3">
      <div className="flex items-start justify-between gap-2">
        <div>
          <div className="font-mono text-sm font-bold">{pool.name}</div>
          <div className="font-mono text-[10px] text-muted-foreground mt-0.5">
            queue: {pool.volcanoQueue} · weight {pool.weight} · {pool.reclaimable ? 'reclaimable' : 'non-reclaimable'}
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <span className={`font-mono text-xs font-bold ${SC_COLORS[pool.serviceClass]}`}>{pool.serviceClass}</span>
          <Badge className={`text-[10px] border flex items-center gap-1 ${stCfg.badge}`}>
            {stCfg.icon}{stCfg.label}
          </Badge>
        </div>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
        {/* CPU */}
        <div className="space-y-1">
          <div className="flex justify-between text-xs">
            <span className="text-muted-foreground">CPU</span>
            <span className="font-mono">{pool.allocatedCPU} / {pool.quota.cpuMax} cores</span>
          </div>
          <Progress value={cpuPct(pool)} className="h-1.5" />
          <div className="text-[10px] text-muted-foreground font-mono">min {pool.quota.cpuMin}</div>
        </div>
        {/* Memory */}
        <div className="space-y-1">
          <div className="flex justify-between text-xs">
            <span className="text-muted-foreground">Memory</span>
            <span className="font-mono">{pool.allocatedMemoryGi} / {pool.quota.memoryMaxGi} Gi</span>
          </div>
          <Progress value={memPct(pool)} className="h-1.5" />
          <div className="text-[10px] text-muted-foreground font-mono">min {pool.quota.memoryMinGi} Gi</div>
        </div>
        {/* GPU */}
        <div className="space-y-1">
          <div className="flex justify-between text-xs">
            <span className="text-muted-foreground">GPU</span>
            <span className="font-mono">{pool.allocatedGPU} / {pool.quota.gpuMax}</span>
          </div>
          <Progress value={gpuPct(pool)} className="h-1.5" />
          <div className="text-[10px] text-muted-foreground font-mono">min {pool.quota.gpuMin}</div>
        </div>
      </div>

      <div className="text-xs text-muted-foreground">
        Pods: <span className="font-mono text-foreground">{pool.podCount}</span>
      </div>
    </div>
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

  return (
    <div className="space-y-6">
      {/* Summary */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ChartBar className="text-primary" />
            Elastic LPAR Pools — Volcano Queue
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Pools</div>
              <div className="font-mono text-2xl font-bold text-foreground">{pools.length}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Expanding</div>
              <div className={`font-mono text-2xl font-bold ${expanding > 0 ? 'text-primary' : 'text-foreground'}`}>{expanding}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Contracting</div>
              <div className={`font-mono text-2xl font-bold ${contracting > 0 ? 'text-[oklch(0.75_0.18_75)]' : 'text-foreground'}`}>{contracting}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">GPU Allocated</div>
              <div className="font-mono text-2xl font-bold text-accent">{totalGPU} / {totalGPUMax}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">CPU Allocated</div>
              <div className="font-mono text-2xl font-bold text-foreground">{totalCPU} / {totalCPUMax}</div>
            </div>
          </div>

          <div className="space-y-2">
            <div className="space-y-1">
              <div className="flex justify-between text-xs text-muted-foreground">
                <span className="uppercase tracking-wide">Cluster CPU</span>
                <span className="font-mono">{totalCPU} / {totalCPUMax} cores</span>
              </div>
              <Progress value={totalCPUMax > 0 ? (totalCPU / totalCPUMax) * 100 : 0} className="h-2" />
            </div>
            <div className="space-y-1">
              <div className="flex justify-between text-xs text-muted-foreground">
                <span className="uppercase tracking-wide">Cluster Memory</span>
                <span className="font-mono">{totalMemGi} / {totalMemMaxGi} Gi</span>
              </div>
              <Progress value={totalMemMaxGi > 0 ? (totalMemGi / totalMemMaxGi) * 100 : 0} className="h-2" />
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Pool rows */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Pool Details</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {pools.map(p => <PoolRow key={p.name} pool={p} />)}
        </CardContent>
      </Card>

      {/* GitOps reference */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">GitOps Queue Patch Reference</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">{`# Source of truth:
# deploy/kubernetes/scheduling/elastic-lpar-pools.yaml
#
# Apply/reconcile:
kubectl apply -f deploy/kubernetes/scheduling/elastic-lpar-pools.yaml
#
# Runtime expand/contract patch example:
kubectl patch queue lpar-training-low \\
  --type=merge \\
  -p '{"spec":{"capability":{"cpu":"384"}}}'`}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
