import { GpuInfo, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Speedometer, ArrowClockwise, Thermometer, Lightning } from '@phosphor-icons/react'

const frame = createFrameClient()

export function GpuView() {
  const { state, reload } = useLiveResource<GpuInfo[]>(() => frame.cluster.gpus())
  const gpus = state.phase === 'ready' ? state.data : []

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Speedometer className="text-primary" />
            GPU
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reload} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {state.phase === 'ready' && (
          <CardContent className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <Stat label="GPUs" value={`${gpus.length}`} tone="accent" />
            <Stat label="Avg util" value={`${gpus.length ? Math.round(gpus.reduce((s, g) => s + g.utilPct, 0) / gpus.length) : 0}%`} />
            <Stat label="Total power" value={`${gpus.reduce((s, g) => s + g.powerW, 0).toFixed(0)} W`} />
            <Stat label="Model" value={gpus[0]?.model ?? '—'} small />
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No GPUs / DCGM exporter not present." />

      {state.phase === 'ready' &&
        gpus.map((g) => {
          const memPct = g.memTotalMB ? (g.memUsedMB / g.memTotalMB) * 100 : 0
          return (
            <Card key={`${g.node}-${g.index}`}>
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div>
                    <CardTitle className="font-mono text-lg">{g.model} #{g.index}</CardTitle>
                    <p className="text-xs text-muted-foreground font-mono">{g.node}</p>
                  </div>
                  <div className="flex gap-2">
                    <Badge variant="outline" className="font-mono text-[10px] gap-1"><Thermometer size={11} />{g.tempC}°C</Badge>
                    <Badge variant="outline" className="font-mono text-[10px] gap-1"><Lightning size={11} />{g.powerW.toFixed(0)}W</Badge>
                  </div>
                </div>
              </CardHeader>
              <CardContent className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <Meter label="GPU utilisation" pct={g.utilPct} detail={`${g.utilPct}%`} />
                <Meter label="Memory" pct={memPct} detail={`${(g.memUsedMB / 1024).toFixed(1)} / ${(g.memTotalMB / 1024).toFixed(1)} GiB`} />
                <div className="grid grid-cols-2 gap-2 text-[10px] text-muted-foreground font-mono sm:col-span-2">
                  <div><span className="text-foreground font-bold">{g.encUtil}%</span> encoder</div>
                  <div><span className="text-foreground font-bold">{g.decUtil}%</span> decoder</div>
                </div>
              </CardContent>
            </Card>
          )
        })}
    </div>
  )
}

function Stat({ label, value, tone, small }: { label: string; value: string; tone?: 'accent'; small?: boolean }) {
  return (
    <div className="space-y-1">
      <div className="text-xs text-muted-foreground uppercase tracking-wide">{label}</div>
      <div className={`font-mono font-bold ${small ? 'text-sm' : 'text-2xl'} ${tone === 'accent' ? 'text-accent' : 'text-foreground'}`}>{value}</div>
    </div>
  )
}

function Meter({ label, pct, detail }: { label: string; pct: number; detail: string }) {
  return (
    <div className="space-y-1">
      <div className="flex justify-between text-xs font-mono">
        <span className="text-muted-foreground">{label}</span>
        <span className="text-foreground">{detail}</span>
      </div>
      <Progress value={Math.max(0, Math.min(100, pct))} className="h-2" />
    </div>
  )
}
