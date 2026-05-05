import { PipelineTrace } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { ArrowRight } from '@phosphor-icons/react'

interface DataLineageViewProps {
  traces?: PipelineTrace[]
}

// Demo traces
const DEMO_TRACES: PipelineTrace[] = [
  {
    traceId: 'trace-abc-001',
    pipelineName: 'neura-training-v3',
    startTime: Date.now() - 7100000,
    totalDurationMs: 7100000,
    serviceClass: 'LOW',
    spans: [
      { spanId: 's1', operationName: 'data-validation', startTime: Date.now() - 7100000, durationMs: 200000, status: 'ok', tags: { dataset: 'neura-training-v3', rows: '12M' } },
      { spanId: 's2', operationName: 'alluxio:cache-load', startTime: Date.now() - 6900000, durationMs: 1500000, status: 'ok', tags: { tier: 'nvme', size: '12.4TB', hit: 'false' } },
      { spanId: 's3', operationName: 'distributed-training', startTime: Date.now() - 5400000, durationMs: 5400000, status: 'ok', tags: { gpus: '8', framework: 'pytorch', nccl: 'ib' } },
    ],
  },
  {
    traceId: 'trace-def-002',
    pipelineName: 'neura-inference-batch-001',
    startTime: Date.now() - 3590000,
    totalDurationMs: 50000,
    serviceClass: 'HIGH',
    spans: [
      { spanId: 'a1', operationName: 'validate-input', startTime: Date.now() - 3590000, durationMs: 10000, status: 'ok', tags: { batch: '64' } },
      { spanId: 'a2', operationName: 'redis:cache-lookup', startTime: Date.now() - 3580000, durationMs: 5000, status: 'ok', tags: { hit: 'true', ttl: '3600s' } },
      { spanId: 'a3', operationName: 'model-inference', startTime: Date.now() - 3575000, durationMs: 25000, status: 'ok', tags: { gpu: 'A100-MIG-2g.20gb', model: 'neura-v2' } },
      { spanId: 'a4', operationName: 'emit-result', startTime: Date.now() - 3550000, durationMs: 10000, status: 'ok', tags: { tokens: '512' } },
    ],
  },
  {
    traceId: 'trace-ghi-003',
    pipelineName: 'neura-training-v2-retry',
    startTime: Date.now() - 14000000,
    totalDurationMs: 4000000,
    serviceClass: 'LOW',
    spans: [
      { spanId: 'b1', operationName: 'data-validation', startTime: Date.now() - 14000000, durationMs: 200000, status: 'ok', tags: { dataset: 'neura-training-v2' } },
      { spanId: 'b2', operationName: 'distributed-training', startTime: Date.now() - 13800000, durationMs: 3800000, status: 'error', tags: { error: 'NCCL timeout', rank: '3' } },
    ],
  },
]

const CLASS_BADGE: Record<string, string> = {
  HIGH:   'bg-destructive/20 text-destructive border-destructive/30',
  MEDIUM: 'bg-[oklch(0.75_0.18_75)]/10 text-[oklch(0.75_0.18_75)] border-[oklch(0.75_0.18_75)]/30',
  LOW:    'bg-accent/20 text-accent border-accent/30',
}

function formatMs(ms: number): string {
  if (ms >= 3600000) return `${(ms / 3600000).toFixed(1)}h`
  if (ms >= 60000) return `${(ms / 60000).toFixed(1)}m`
  if (ms >= 1000) return `${(ms / 1000).toFixed(1)}s`
  return `${ms}ms`
}

function TraceTimeline({ trace }: { trace: PipelineTrace }) {
  const totalMs = trace.totalDurationMs || 1

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="font-mono font-bold text-sm">{trace.pipelineName}</span>
          <Badge className={`text-[10px] border ${CLASS_BADGE[trace.serviceClass]}`}>{trace.serviceClass}</Badge>
        </div>
        <div className="text-xs text-muted-foreground font-mono">
          {formatMs(totalMs)} total · {new Date(trace.startTime).toLocaleTimeString()}
        </div>
      </div>

      {/* Gantt-style timeline */}
      <div className="space-y-1.5">
        {trace.spans.map(span => {
          const offset = ((span.startTime - trace.startTime) / totalMs) * 100
          const width = Math.max(0.5, (span.durationMs / totalMs) * 100)
          return (
            <div key={span.spanId} className="space-y-0.5">
              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                <span className="font-mono w-48 truncate">{span.operationName}</span>
                <span className="font-mono text-[10px]">{formatMs(span.durationMs)}</span>
              </div>
              <div className="w-full h-5 bg-secondary rounded relative">
                <div
                  className={`absolute h-5 rounded text-[9px] font-mono flex items-center px-1 overflow-hidden ${
                    span.status === 'error' ? 'bg-destructive/60 text-destructive-foreground' : 'bg-primary/60 text-primary-foreground'
                  }`}
                  style={{ left: `${offset}%`, width: `${width}%`, minWidth: '4px' }}
                  title={`${span.operationName}: ${formatMs(span.durationMs)}`}
                />
              </div>
            </div>
          )
        })}
      </div>

      {/* Lineage DAG text */}
      <div className="flex items-center gap-1 flex-wrap text-xs text-muted-foreground font-mono">
        {trace.spans.map((span, i) => (
          <span key={span.spanId} className="flex items-center gap-1">
            <span className={span.status === 'error' ? 'text-destructive' : 'text-foreground'}>{span.operationName}</span>
            {i < trace.spans.length - 1 && <ArrowRight size={10} />}
          </span>
        ))}
      </div>

      {/* Tags for notable spans */}
      <div className="flex flex-wrap gap-1">
        {trace.spans
          .filter(s => Object.keys(s.tags).length > 0)
          .flatMap(s => Object.entries(s.tags).map(([k, v]) => (
            <span key={`${s.spanId}-${k}`} className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-secondary border border-border">
              {k}: {v}
            </span>
          )))
          .slice(0, 8)}
      </div>
    </div>
  )
}

export function DataLineageView({ traces = DEMO_TRACES }: DataLineageViewProps) {
  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ArrowRight className="text-primary" />
            Data Lineage — Pipeline Traces
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          <div className="grid grid-cols-3 gap-4 text-center">
            <div>
              <div className="font-mono text-2xl font-bold text-primary">{traces.length}</div>
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Total Traces</div>
            </div>
            <div>
              <div className="font-mono text-2xl font-bold text-accent">{traces.filter(t => t.spans.every(s => s.status === 'ok')).length}</div>
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Successful</div>
            </div>
            <div>
              <div className="font-mono text-2xl font-bold text-destructive">{traces.filter(t => t.spans.some(s => s.status === 'error')).length}</div>
              <div className="text-xs text-muted-foreground uppercase tracking-wide">With Errors</div>
            </div>
          </div>
        </CardContent>
      </Card>

      <div className="space-y-4">
        {traces.map(trace => (
          <Card key={trace.traceId}>
            <CardContent className="pt-4">
              <TraceTimeline trace={trace} />
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  )
}
