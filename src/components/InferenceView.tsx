import { InferenceStatus, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Lightning, ArrowClockwise, Database, Gauge } from '@phosphor-icons/react'

const frame = createFrameClient()

export function InferenceView() {
  const { state, reload } = useLiveResource<InferenceStatus | null>(() => frame.cluster.inference())
  const inf = state.phase === 'ready' ? state.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Lightning className="text-primary" />
            KV-Cache & Inference
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {inf && (
          <CardContent className="flex flex-wrap gap-2 text-xs">
            <Badge variant="outline" className="font-mono gap-1"><Database size={12} />{inf.model}</Badge>
            <Badge variant="outline" className="font-mono">{inf.node}</Badge>
            <Badge variant="outline" className="font-mono">{inf.slots} slots</Badge>
            <Badge variant="outline" className="font-mono">ctx {inf.nCtx}</Badge>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No inference server (llama.cpp) deployed." />

      {inf && (
        <>
          <Card>
            <CardHeader><CardTitle className="font-mono text-base">KV cache</CardTitle></CardHeader>
            <CardContent className="space-y-3">
              <div className="flex justify-between text-xs font-mono">
                <span className="text-muted-foreground">Used tokens</span>
                <span>{inf.kvTokens} / {inf.nCtx} ({inf.kvUsePct.toFixed(1)}%)</span>
              </div>
              <Progress value={Math.max(0, Math.min(100, inf.kvUsePct))} className="h-2" />
              <p className="text-[11px] text-muted-foreground font-mono">Peak KV depth observed vs context window.</p>
            </CardContent>
          </Card>

          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <Stat icon={<Gauge size={14} />} label="Prompt tok/s" value={inf.promptTokensPerSec.toFixed(1)} />
            <Stat icon={<Gauge size={14} />} label="Decode tok/s" value={inf.predictedTokensPerSec.toFixed(1)} tone="accent" />
            <Stat label="Processing" value={`${inf.requestsProcessing}`} />
            <Stat label="Deferred" value={`${inf.requestsDeferred}`} />
            <Stat label="Prompt tokens" value={inf.promptTokensTotal.toLocaleString()} small />
            <Stat label="Decoded tokens" value={inf.tokensPredictedTotal.toLocaleString()} small />
            <Stat label="Busy slots/decode" value={inf.busySlotsPerDecode.toFixed(2)} small />
          </div>
        </>
      )}
    </div>
  )
}

function Stat({ icon, label, value, tone, small }: { icon?: React.ReactNode; label: string; value: string; tone?: 'accent'; small?: boolean }) {
  return (
    <Card>
      <CardContent className="pt-6 space-y-1">
        <div className="text-xs text-muted-foreground uppercase tracking-wide flex items-center gap-1">{icon}{label}</div>
        <div className={`font-mono font-bold ${small ? 'text-base' : 'text-2xl'} ${tone === 'accent' ? 'text-accent' : 'text-foreground'}`}>{value}</div>
      </CardContent>
    </Card>
  )
}
