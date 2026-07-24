import { PipelinePPMetrics, PPStageStats, PPStageRole } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { EntityTile, MicroStats, StatTile, TuningDashboard } from '@/components/TuningDashboard'
import { TONE_TEXT, Tone, inverseScoreTone, scoreTone } from '@/lib/thresholds'
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

/** Bubble ratio is stall time — lower is better (accent ≤ 0.1, warning ≤ 0.2). */
const bubbleTone = (ratio: number): Tone => inverseScoreTone(ratio, 0.1, 0.2)

/** Stage utilisation: higher is better — accent ≥ 90%, warning ≥ 75%. */
const utilTone = (pct: number): Tone => scoreTone(pct, 90, 75)

const HIGH_BUBBLE = 0.2

// ── Stage card ────────────────────────────────────────────────────────────────

function StageCard({ stage }: { stage: PPStageStats }) {
  const cfg = ROLE_CONFIG[stage.role]
  const highBubble = stage.bubbleRatio >= HIGH_BUBBLE

  return (
    <EntityTile
      className={highBubble ? 'border-destructive/40 bg-destructive/5' : undefined}
      name={
        <span className="flex items-center gap-2">
          {highBubble && <Warning size={12} className="text-destructive" />}
          <span>Stage {stage.stageId}</span>
          <span className="font-normal text-[10px] text-muted-foreground">GPU {stage.gpuIndices.join(',')}</span>
        </span>
      }
      badge={<Badge className={`text-[10px] border ${cfg.badge}`}>{cfg.label}</Badge>}
    >
      {/* Not a TileMeter: the bar is tinted per pipeline role, not per tone. */}
      <div className="space-y-1">
        <div className="flex justify-between text-xs">
          <span className="text-muted-foreground">Utilization</span>
          <span className={`font-mono font-bold ${TONE_TEXT[utilTone(stage.utilizationPct)]}`}>{stage.utilizationPct}%</span>
        </div>
        <div className="w-full h-1.5 rounded-full bg-secondary">
          <div className={`h-1.5 rounded-full ${cfg.bar}`} style={{ width: `${stage.utilizationPct}%` }} />
        </div>
      </div>

      <MicroStats
        items={[
          { label: 'Bubble', value: `${(stage.bubbleRatio * 100).toFixed(0)}%`, tone: bubbleTone(stage.bubbleRatio) },
          { label: 'µ-batches', value: stage.microBatchesInFlight },
          { label: 'Tok/s', value: `${(stage.tokensPerSec / 1000).toFixed(1)}k`, tone: 'primary' },
        ]}
      />
    </EntityTile>
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

  const summary: StatTile[] = [
    { label: 'PP Degree', value: m.pipelineDegree },
    { label: 'TP Degree', value: m.tensorParallelDegree },
    {
      label: 'Efficiency',
      value: `${(m.efficiency * 100).toFixed(0)}%`,
      // Two-band ladder: accent ≥ 85%, warning below.
      tone: scoreTone(m.efficiency, 0.85, 0),
    },
    {
      label: 'E2E Latency',
      value: <><Lightning size={18} className="inline" /> {m.e2eLatencyMs} ms</>,
      tone: 'primary',
    },
    { label: 'Pipeline Tok/s', value: `${(totalTPS / 1000).toFixed(1)}k`, tone: 'accent' },
  ]

  return (
    <TuningDashboard
      title="Pipeline Parallelism — Prefill / Decode"
      icon={<GitBranch className="text-primary" />}
      stats={summary}
      progress={{
        value: m.efficiency * 100,
        captionLeft: <span className="uppercase tracking-wide">Pipeline Efficiency (1 − avg bubble)</span>,
        captionRight: <span className="font-mono">{(m.efficiency * 100).toFixed(0)}%</span>,
      }}
      note={<PipelineDiagram metrics={m} />}
      entitiesTitle="Stage Details"
      entities={m.stages}
      entityKey={(s) => String(s.stageId)}
      renderEntity={(s) => <StageCard stage={s} />}
      entityColumns={4}
      configTitle="Deployment Config Reference"
      config={`# deploy/jobs/pipeline-parallelism.yaml  (excerpt)
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
  - '{"kv_connector":"NixlConnector","kv_role":"kv_producer","kv_rank":0}'`}
    />
  )
}
