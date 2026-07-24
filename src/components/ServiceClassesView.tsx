import { ResourceQuota, ServiceClassSummary, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { ShieldCheck, ArrowClockwise } from '@phosphor-icons/react'

const frame = createFrameClient()

type Data = { classes: ServiceClassSummary[]; quotas: ResourceQuota[] }

const CLASS_TONE: Record<string, string> = {
  HIGH: 'text-destructive',
  MEDIUM: 'text-warning',
  LOW: 'text-accent',
}

export function ServiceClassesView() {
  const { state, reload } = useLiveResource<Data>(async () => {
    const [classes, quotas] = await Promise.all([
      frame.resources.listServiceClasses(),
      frame.resources.listQuotas(),
    ])
    return { classes: classes.items, quotas: quotas.items }
  })

  const data = state.phase === 'ready' ? state.data : null

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <ShieldCheck className="text-primary" />
            Service Classes
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={reload}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Live tiers derived from <span className="font-mono">FrameNode</span> service classes and
            their <span className="font-mono">FrameResourceQuota</span> limits.
          </p>
        </CardContent>
      </Card>

      <LiveStates state={state} emptyLabel="No FrameNodes or quotas defined." />

      {data && (
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          {data.classes.map((c) => {
            const quota = data.quotas.find((q) => q.serviceClass === c.serviceClass)
            return (
              <Card key={c.serviceClass}>
                <CardHeader>
                  <CardTitle className={`font-mono text-lg ${CLASS_TONE[c.serviceClass]}`}>
                    {c.serviceClass}
                  </CardTitle>
                </CardHeader>
                <CardContent className="space-y-2 text-xs font-mono">
                  <Row label="Nodes" value={`${c.nodeCount}`} />
                  <Row label="GPUs" value={`${c.totalGPUs}`} />
                  {quota && (
                    <>
                      <Row label="Max CPU" value={quota.maxCPU} />
                      <Row label="Max memory" value={quota.maxMemory} />
                      <Row label="Max GPUs" value={`${quota.maxGPUs}`} />
                    </>
                  )}
                </CardContent>
              </Card>
            )
          })}
        </div>
      )}
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-muted-foreground">{label}</span>
      <Badge variant="outline" className="font-mono text-[10px]">
        {value}
      </Badge>
    </div>
  )
}
