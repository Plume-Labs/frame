import { PipelinePPMetrics, PPStageStats, PPStageRole } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { GitBranch, Lightning, Warning } from '@phosphor-icons/react'

// ── Simulated state ───────────────────────────────────────────────────────────

const MOCK_METRICS: PipelinePPMetrics = {
  pipelineDegree: 4,
  tensorParallelDegree: 2,
  microBatches: 8,
  e2eLatencyMs: 145,
  efficiency: 0.87,
  stages: [
    { stageId: 0, role: 'prefill',  gpuIndices: [0, 1], microBatchesInFlight: 6, utilizationPct: 94, bubbleRatio: 0.08, tokensPerSec: 12400 },
    { stageId: 1, role: 'prefill',  gpuIndices: [2, 3], microBatchesInFlight: 5, utilizationPct: 91, bubbleRatio: 0.10, tokensPerSec: 11800 },
    { stageId: 2, role: 'decode',   gpuIndices: [4, 5], microBatchesInFlight: 7, utilizationPct: 88, bubbleRatio: 0.12, tokensPerSec: 10900 },
    { stageId: 3, role: 'decode',   gpuIndices: [6, 7], microBatchesInFlight: 8, utilizationPct: 85, bubbleRatio: 0.15, tokensPerSec: 10200 },
  ],
}

// ── Helpers ───────────────────────────────────────────────────────────────────

const ROLE_CONFIG: Record<PPStageRole, { label: string; badge: string; bar: string }> = {
  prefill:  { label: 'Prefill',  badge: 'bg-primary/20 text-primary border-primary/30',   bar: 'bg-primary' },
  decode:   { label: 'Decode',   badge: 'bg-accent/20 text-accent border-accent/30',       bar: 'bg-accent' },
  combined: { label: 'Combined', badge: 'bg-secondary text-muted-foreground border-border', bar: 'bg-foreground' },
}

function bubbleColor(ratio: number) {
  if (ratio >= 0.2) return 'text-destructive'
  if (ratio >= 0.1) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-accent'
}

function utilColor(pct: number) {
  if (pct >= 90) return 'text-accent'
  if (pct >= 75) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-destructive'
}

// ── Stage card ────────────────────────────────────────────────────────────────

function StageCard({ stage }: { stage: PPStageStats }) {
  const cfg = ROLE_CONFIG[stage.role]
  const highBubble = stage.bubbleRatio >= 0.2

  return (
    <div className={`p-3 rounded-lg border ${highBubble ? 'border-destructive/40 bg-destructive/5' : 'border-border bg-secondary/30'} space-y-2`}>
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          {highBubble && <Warning size={12} className="text-destructive" />}
          <span className="font-mono text-xs font-bold">Stage {stage.stageId}</span>
          <span className="font-mono text-[10px] text-muted-foreground">GPU {stage.gpuIndices.join(',')}</span>
        </div>
        <Badge className={`text-[10px] border ${cfg.badge}`}>{cfg.label}</Badge>
      </div>

      <div className="space-y-1">
        <div className="flex justify-between text-xs">
          <span className="text-muted-foreground">Utilization</span>
          <span className={`font-mono font-bold ${utilColor(stage.utilizationPct)}`}>{stage.utilizationPct}%</span>
        </div>
        <div className="w-full h-1.5 rounded-full bg-secondary">
          <div className={`h-1.5 rounded-full ${cfg.bar}`} style={{ width: `${stage.utilizationPct}%` }} />
        </div>
      </div>

      <div className="grid grid-cols-3 gap-1 text-[10px] text-muted-foreground">
        <div>
          <span className={`font-mono font-bold ${bubbleColor(stage.bubbleRatio)}`}>{(stage.bubbleRatio * 100).toFixed(0)}%</span>
          <div>Bubble</div>
        </div>
        <div>
          <span className="font-mono font-bold text-foreground">{stage.microBatchesInFlight}</span>
          <div>µ-batches</div>
        </div>
        <div>
          <span className="font-mono font-bold text-primary">{(stage.tokensPerSec / 1000).toFixed(1)}k</span>
          <div>Tok/s</div>
        </div>
      </div>
    </div>
  )
}

// ── Pipeline diagram (text-based) ─────────────────────────────────────────────

function PipelineDiagram({ metrics }: { metrics: PipelinePPMetrics }) {
  return (
    <div className="font-mono text-[10px] text-muted-foreground space-y-2 overflow-x-auto">
      <div className="flex items-center gap-0">
        {metrics.stages.map((s, i) => {
          const cfg = ROLE_CONFIG[s.role]
          const isLast = i === metrics.stages.length - 1
          return (
            <div key={s.stageId} className="flex items-center">
              <div className={`px-3 py-1.5 rounded text-[10px] font-bold border ${s.role === 'prefill' ? 'border-primary/40 text-primary bg-primary/10' : 'border-accent/40 text-accent bg-accent/10'}`}>
                PP{s.stageId}<br />{cfg.label}
              </div>
              {!isLast && <div className="px-1 text-muted-foreground">→</div>}
            </div>
          )
        })}
      </div>
      <div className="text-muted-foreground">
        TP={metrics.tensorParallelDegree} per stage · {metrics.microBatches} micro-batches
      </div>
    </div>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function PipelinePPDashboard() {
  const m = MOCK_METRICS
  const totalTPS = Math.min(...m.stages.map(s => s.tokensPerSec))   // bottleneck = slowest stage

  return (
    <div className="space-y-6">
      {/* Summary */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <GitBranch className="text-primary" />
            Pipeline Parallelism — Prefill / Decode
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">PP Degree</div>
              <div className="font-mono text-2xl font-bold text-foreground">{m.pipelineDegree}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">TP Degree</div>
              <div className="font-mono text-2xl font-bold text-foreground">{m.tensorParallelDegree}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Efficiency</div>
              <div className={`font-mono text-2xl font-bold ${m.efficiency >= 0.85 ? 'text-accent' : 'text-[oklch(0.75_0.18_75)]'}`}>
                {(m.efficiency * 100).toFixed(0)}%
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">E2E Latency</div>
              <div className="font-mono text-2xl font-bold text-primary">
                <Lightning size={18} className="inline" /> {m.e2eLatencyMs} ms
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Pipeline Tok/s</div>
              <div className="font-mono text-2xl font-bold text-accent">{(totalTPS / 1000).toFixed(1)}k</div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="flex justify-between text-xs text-muted-foreground">
              <span className="uppercase tracking-wide">Pipeline Efficiency (1 − avg bubble)</span>
              <span className="font-mono">{(m.efficiency * 100).toFixed(0)}%</span>
            </div>
            <Progress value={m.efficiency * 100} className="h-3" />
          </div>

          <PipelineDiagram metrics={m} />
        </CardContent>
      </Card>

      {/* Per-stage cards */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Stage Details</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
            {m.stages.map(s => <StageCard key={s.stageId} stage={s} />)}
          </div>
        </CardContent>
      </Card>

      {/* Config reference */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Deployment Config Reference</CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">{`# deploy/jobs/pipeline-parallelism.yaml  (excerpt)
# vLLM with PP=4, TP=2 — 8 GPUs total (4 stages × 2-way tensor parallel)
args:
  - --model=meta-llama/Llama-3.1-70B-Instruct
  - --pipeline-parallel-size=4      # PP degree
  - --tensor-parallel-size=2        # TP per stage
  - --num-scheduler-steps=8         # micro-batches in flight
  - --disable-async-output-proc     # required for PP > 1 in vLLM
  # Disaggregated prefill/decode (vLLM >= 0.9 with kv_transfer):
  # Stage 0–1: prefill role; Stage 2–3: decode role
  - --kv-transfer-config
  - '{"kv_connector":"NixlConnector","kv_role":"kv_producer","kv_rank":0}'`}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
