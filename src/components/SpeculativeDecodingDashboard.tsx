import { SpecDecodeMetrics, SpecDecodeRouteStats, SpecDecodeStrategy } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Lightning, ArrowsLeftRight, ChartLine } from '@phosphor-icons/react'

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

function acceptColor(rate: number) {
  if (rate >= 0.7) return 'text-accent'
  if (rate >= 0.5) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-destructive'
}

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
    <div className="p-3 rounded-lg bg-secondary/30 border border-border space-y-2">
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-xs font-semibold">{route.path}</span>
        {isSpecActive
          ? <Badge className="text-[10px] border bg-accent/20 text-accent border-accent/30">speculative</Badge>
          : <Badge className="text-[10px] border bg-secondary text-muted-foreground border-border">baseline only</Badge>
        }
      </div>

      {isSpecActive && (
        <>
          <div className="space-y-0.5">
            <div className="flex justify-between text-xs">
              <span className="text-muted-foreground">Speculative fraction</span>
              <span className="font-mono">{(route.speculativeFraction * 100).toFixed(0)}%</span>
            </div>
            <Progress value={route.speculativeFraction * 100} className="h-1" />
          </div>

          <div className="grid grid-cols-3 gap-2 text-[10px] text-muted-foreground">
            <div>
              <span className="font-mono font-bold text-accent">{route.speculativeLatencyMs} ms</span>
              <div>Spec latency</div>
            </div>
            <div>
              <span className="font-mono font-bold text-foreground">{route.baselineLatencyMs} ms</span>
              <div>Baseline latency</div>
            </div>
            {dl && (
              <div>
                <span className="font-mono font-bold text-accent">−{dl.pct.toFixed(0)}%</span>
                <div>Saved ({dl.delta} ms)</div>
              </div>
            )}
          </div>
        </>
      )}

      {!isSpecActive && (
        <div className="text-[10px] text-muted-foreground">
          Baseline: <span className="font-mono text-foreground">{route.baselineLatencyMs} ms</span>
        </div>
      )}
    </div>
  )
}

// ── Main dashboard ────────────────────────────────────────────────────────────

export function SpeculativeDecodingDashboard() {
  const m = MOCK_METRICS

  return (
    <div className="space-y-6">
      {/* Summary */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Lightning className="text-primary" />
            Speculative Decoding
            <Badge className="ml-2 text-xs border bg-primary/20 text-primary border-primary/30">
              {STRATEGY_LABELS[m.strategy]}
            </Badge>
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Throughput Speedup</div>
              <div className="font-mono text-2xl font-bold text-accent">×{m.throughputSpeedup.toFixed(1)}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Token Acceptance</div>
              <div className={`font-mono text-2xl font-bold ${acceptColor(m.tokenAcceptanceRate)}`}>
                {(m.tokenAcceptanceRate * 100).toFixed(0)}%
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Mean Draft Tokens</div>
              <div className="font-mono text-2xl font-bold text-primary">{m.meanDraftTokens.toFixed(1)}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Active Spec Requests</div>
              <div className="font-mono text-2xl font-bold text-foreground">{m.activeSpecRequests}</div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="flex justify-between text-xs text-muted-foreground">
              <span className="uppercase tracking-wide">Token Acceptance Rate</span>
              <span className="font-mono">{(m.tokenAcceptanceRate * 100).toFixed(0)}%</span>
            </div>
            <Progress value={m.tokenAcceptanceRate * 100} className="h-3" />
          </div>

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
        </CardContent>
      </Card>

      {/* Route table */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <ArrowsLeftRight className="text-primary" />
            Frame Routing — per Endpoint
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {m.routes.map(r => <RouteRow key={r.path} route={r} />)}
        </CardContent>
      </Card>

      {/* Deployment reference */}
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg flex items-center gap-2">
            <ChartLine className="text-primary" />
            Deployment Reference
          </CardTitle>
        </CardHeader>
        <CardContent>
          <pre className="text-[11px] font-mono bg-secondary/40 rounded-lg p-4 overflow-x-auto text-muted-foreground leading-relaxed">{`# deploy/jobs/speculative-decoding.yaml  (excerpt)
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
    frame.plume-labs.io/speculative-routes: "/v1/chat/completions,/v1/completions"`}</pre>
        </CardContent>
      </Card>
    </div>
  )
}
