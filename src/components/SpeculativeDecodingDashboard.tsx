import { SpecDecodeMetrics, SpecDecodeRouteStats, SpecDecodeStrategy } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { EntityTile, MicroStats, StatTile, TileMeter, TuningDashboard } from '@/components/TuningDashboard'
import { Tone, scoreTone } from '@/lib/thresholds'
import { Lightning } from '@phosphor-icons/react'

// ── Simulated state ───────────────────────────────────────────────────────────

const MOCK_METRICS: SpecDecodeMetrics = {
  strategy: 'eagle',
  draftModel: 'meta-llama/Llama-3.2-1B',
  targetModel: 'meta-llama/Llama-3.1-70B-Instruct',
  meanDraftTokens: 4.7,
  tokenAcceptanceRate: 0.72,
  throughputSpeedup: 2.4,
  totalRequests: 184320,
  activeSpecRequests: 87,
  routes: [
    { path: '/v1/chat/completions', speculativeFraction: 0.91, speculativeLatencyMs: 210, baselineLatencyMs: 520 },
    { path: '/v1/completions',      speculativeFraction: 0.78, speculativeLatencyMs: 195, baselineLatencyMs: 490 },
    { path: '/v1/embeddings',       speculativeFraction: 0.00, speculativeLatencyMs: 0,   baselineLatencyMs: 38  },
  ],
}

// ── Helpers ───────────────────────────────────────────────────────────────────

const STRATEGY_LABELS: Record<SpecDecodeStrategy, string> = {
  'draft-model': 'Draft Model',
  medusa:        'Medusa',
  eagle:         'EAGLE',
}

/** Token acceptance: higher is better — accent ≥ 70%, warning ≥ 50%. */
const acceptTone = (rate: number): Tone => scoreTone(rate, 0.7, 0.5)

function latencyDelta(route: SpecDecodeRouteStats) {
  if (route.speculativeFraction === 0 || route.speculativeLatencyMs === 0) return null
  const delta = route.baselineLatencyMs - route.speculativeLatencyMs
  const pct   = (delta / route.baselineLatencyMs) * 100
  return { delta, pct }
}

// ── Route row ─────────────────────────────────────────────────────────────────

function RouteRow({ route }: { route: SpecDecodeRouteStats }) {
  const dl = latencyDelta(route)
  const isSpecActive = route.speculativeFraction > 0

  return (
    <EntityTile
      name={route.path}
      badge={
        isSpecActive
          ? <Badge className="text-[10px] border bg-accent/20 text-accent border-accent/30">speculative</Badge>
          : <Badge className="text-[10px] border bg-secondary text-muted-foreground border-border">baseline only</Badge>
      }
    >
      {isSpecActive ? (
        <>
          <TileMeter
            label="Speculative fraction"
            value={route.speculativeFraction * 100}
            tone="foreground"
          />

          <MicroStats
            items={[
              { label: 'Spec latency', value: `${route.speculativeLatencyMs} ms`, tone: 'accent' },
              { label: 'Baseline latency', value: `${route.baselineLatencyMs} ms` },
              ...(dl ? [{ label: `Saved (${dl.delta} ms)`, value: `−${dl.pct.toFixed(0)}%`, tone: 'accent' as Tone }] : []),
            ]}
          />
        </>
      ) : (
        <div className="text-[10px] text-muted-foreground">
          Baseline: <span className="font-mono text-foreground">{route.baselineLatencyMs} ms</span>
        </div>
      )}
    </EntityTile>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function SpeculativeDecodingDashboard() {
  const m = MOCK_METRICS

  const summary: StatTile[] = [
    { label: 'Throughput Speedup', value: `×${m.throughputSpeedup.toFixed(1)}`, tone: 'accent' },
    {
      label: 'Token Acceptance',
      value: `${(m.tokenAcceptanceRate * 100).toFixed(0)}%`,
      tone: acceptTone(m.tokenAcceptanceRate),
    },
    { label: 'Mean Draft Tokens', value: m.meanDraftTokens.toFixed(1), tone: 'primary' },
    { label: 'Active Spec Requests', value: m.activeSpecRequests },
  ]

  return (
    <TuningDashboard
      title="Speculative Decoding"
      icon={<Lightning className="text-primary" />}
      badge={
        <Badge className="text-xs border bg-primary/20 text-primary border-primary/30">
          {STRATEGY_LABELS[m.strategy]}
        </Badge>
      }
      stats={summary}
      progress={{
        value: m.tokenAcceptanceRate * 100,
        captionLeft: <span className="uppercase tracking-wide">Token Acceptance Rate</span>,
        captionRight: <span className="font-mono">{(m.tokenAcceptanceRate * 100).toFixed(0)}%</span>,
      }}
      note={
        <>
          <div className="rounded-lg bg-secondary/40 p-3 grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
            <div className="space-y-0.5">
              <div className="text-muted-foreground uppercase tracking-wide text-[10px]">Draft Model</div>
              <div className="font-mono text-foreground">{m.draftModel}</div>
            </div>
            <div className="space-y-0.5">
              <div className="text-muted-foreground uppercase tracking-wide text-[10px]">Target Model</div>
              <div className="font-mono text-foreground">{m.targetModel}</div>
            </div>
          </div>

          <div className="text-xs text-muted-foreground">
            Total requests served: <span className="font-mono text-foreground">{m.totalRequests.toLocaleString()}</span>
          </div>
        </>
      }
      entitiesTitle="Frame Routing — per Endpoint"
      entities={m.routes}
      entityKey={(r) => r.path}
      renderEntity={(r) => <RouteRow route={r} />}
      entityLayout="stack"
      configTitle="Deployment Reference"
      config={`# deploy/jobs/speculative-decoding.yaml  (excerpt)
# vLLM engine args — enable EAGLE speculative decoding
args:
  - --model=${m.targetModel}
  - --speculative-model=${m.draftModel}
  - --num-speculative-tokens=5
  - --speculative-draft-tensor-parallel-size=1
  - --use-v2-block-manager

# Frame routing annotation (InferenceService CR)
metadata:
  annotations:
    frame.plume-labs.io/speculative-decoding: "enabled"
    frame.plume-labs.io/speculative-strategy: "eagle"
    frame.plume-labs.io/speculative-routes: "/v1/chat/completions,/v1/completions"`}
    />
  )
}
