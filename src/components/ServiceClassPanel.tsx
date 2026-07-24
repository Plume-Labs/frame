import { ClusterNode, ServiceClass } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Gauge, CheckCircle, Warning, XCircle } from '@phosphor-icons/react'

interface ServiceClassPanelProps {
  nodes: ClusterNode[]
}

interface SLATarget {
  label: string
  latencyMs: number
  availabilityPct: number
}

const SLA_TARGETS: Record<ServiceClass, SLATarget> = {
  HIGH:   { label: 'Real-time Inference', latencyMs: 50,   availabilityPct: 99.99 },
  MEDIUM: { label: 'Database / Query',    latencyMs: 200,  availabilityPct: 99.9  },
  LOW:    { label: 'Batch / Training',    latencyMs: 5000, availabilityPct: 99.0  },
}

const CLASS_COLORS: Record<ServiceClass, { border: string; badge: string; text: string }> = {
  HIGH:   { border: 'border-destructive/40',                   badge: 'bg-destructive/20 text-destructive border-destructive/30',   text: 'text-destructive' },
  MEDIUM: { border: 'border-warning/40',        badge: 'bg-warning/10 text-warning border-warning/30', text: 'text-warning' },
  LOW:    { border: 'border-accent/40',                        badge: 'bg-accent/20 text-accent border-accent/30',                  text: 'text-accent' },
}

// Simulated current latency and availability
const CURRENT_METRICS: Record<ServiceClass, { latencyMs: number; availabilityPct: number; quotaUsed: number }> = {
  HIGH:   { latencyMs: 38,   availabilityPct: 99.99, quotaUsed: 0.82 },
  MEDIUM: { latencyMs: 145,  availabilityPct: 99.94, quotaUsed: 0.61 },
  LOW:    { latencyMs: 3200, availabilityPct: 99.1,  quotaUsed: 0.45 },
}

function StatusIcon({ ok }: { ok: boolean }) {
  return ok
    ? <CheckCircle className="text-accent" size={16} />
    : <Warning className="text-warning" size={16} />
}

export function ServiceClassPanel({ nodes }: ServiceClassPanelProps) {
  const classes: ServiceClass[] = ['HIGH', 'MEDIUM', 'LOW']

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Gauge className="text-primary" />
            Service Classes — SLA Status
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {classes.map(cls => {
            const sla = SLA_TARGETS[cls]
            const cur = CURRENT_METRICS[cls]
            const colors = CLASS_COLORS[cls]
            const latencyOk = cur.latencyMs <= sla.latencyMs
            const availOk = cur.availabilityPct >= sla.availabilityPct
            const clsNodes = nodes.filter(n => n.serviceClass === cls)
            const onlineClsNodes = clsNodes.filter(n => n.status === 'online')

            return (
              <div key={cls} className={`p-4 rounded-lg bg-secondary/30 border ${colors.border}`}>
                <div className="flex items-center justify-between mb-3">
                  <div className="flex items-center gap-2">
                    <Badge className={`font-mono font-bold border ${colors.badge}`}>{cls}</Badge>
                    <span className="text-sm text-muted-foreground">{sla.label}</span>
                  </div>
                  <div className="flex items-center gap-1">
                    {latencyOk && availOk
                      ? <CheckCircle className="text-accent" size={18} />
                      : <XCircle className="text-warning" size={18} />}
                    <span className="text-xs font-mono text-muted-foreground">
                      {latencyOk && availOk ? 'SLA MET' : 'SLA BREACH'}
                    </span>
                  </div>
                </div>

                <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-3">
                  <div className="space-y-1">
                    <div className="text-xs text-muted-foreground uppercase">Latency P99</div>
                    <div className="flex items-center gap-1">
                      <StatusIcon ok={latencyOk} />
                      <span className={`font-mono text-sm font-bold ${latencyOk ? 'text-foreground' : 'text-warning'}`}>
                        {cur.latencyMs} ms
                      </span>
                    </div>
                    <div className="text-xs text-muted-foreground">target ≤ {sla.latencyMs} ms</div>
                  </div>

                  <div className="space-y-1">
                    <div className="text-xs text-muted-foreground uppercase">Availability</div>
                    <div className="flex items-center gap-1">
                      <StatusIcon ok={availOk} />
                      <span className={`font-mono text-sm font-bold ${availOk ? 'text-foreground' : 'text-warning'}`}>
                        {cur.availabilityPct.toFixed(2)}%
                      </span>
                    </div>
                    <div className="text-xs text-muted-foreground">target {sla.availabilityPct}%</div>
                  </div>

                  <div className="space-y-1">
                    <div className="text-xs text-muted-foreground uppercase">Quota Used</div>
                    <div className={`font-mono text-sm font-bold ${cur.quotaUsed > 0.9 ? 'text-warning' : 'text-foreground'}`}>
                      {(cur.quotaUsed * 100).toFixed(0)}%
                    </div>
                    <Progress value={cur.quotaUsed * 100} className="h-1" />
                  </div>

                  <div className="space-y-1">
                    <div className="text-xs text-muted-foreground uppercase">Nodes</div>
                    <div className={`font-mono text-sm font-bold ${colors.text}`}>
                      {onlineClsNodes.length} / {clsNodes.length}
                    </div>
                    <div className="text-xs text-muted-foreground">online</div>
                  </div>
                </div>
              </div>
            )
          })}
        </CardContent>
      </Card>
    </div>
  )
}
