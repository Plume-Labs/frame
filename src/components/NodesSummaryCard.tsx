import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ClusterNode } from '@/lib/types'
import { CheckCircle, XCircle, Warning, Circle } from '@phosphor-icons/react'
import { Progress } from '@/components/ui/progress'

interface NodesSummaryCardProps {
  nodes: ClusterNode[]
}

export function NodesSummaryCard({ nodes }: NodesSummaryCardProps) {
  const statusCounts = nodes.reduce((acc, node) => {
    acc[node.status] = (acc[node.status] || 0) + 1
    return acc
  }, {} as Record<string, number>)

  const totalNodes = nodes.length
  const onlineNodes = statusCounts.online || 0
  const healthPercentage = (onlineNodes / totalNodes) * 100

  const avgCpu = nodes.reduce((sum, node) => sum + node.metrics.cpu, 0) / totalNodes
  const avgMemory = nodes.reduce((sum, node) => sum + node.metrics.memory, 0) / totalNodes
  const avgStorage = nodes.reduce((sum, node) => sum + node.metrics.storage, 0) / totalNodes

  const zones = [...new Set(nodes.map(n => n.zone))]
  const racks = [...new Set(nodes.map(n => n.rackId))]

  const getStatusColor = (status: string) => {
    switch (status) {
      case 'online': return 'text-primary'
      case 'degraded': return 'text-warning'
      case 'offline': return 'text-destructive'
      case 'provisioning': return 'text-muted-foreground'
      default: return 'text-muted-foreground'
    }
  }

  const getStatusIcon = (status: string) => {
    switch (status) {
      case 'online': return CheckCircle
      case 'degraded': return Warning
      case 'offline': return XCircle
      case 'provisioning': return Circle
      default: return Circle
    }
  }

  return (
    <Card className="border-border/50 bg-card/50 backdrop-blur">
      <CardHeader>
        <CardTitle className="font-mono text-lg">Node Fleet Summary</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="grid grid-cols-2 gap-4">
          <div className="space-y-1">
            <div className="text-sm text-muted-foreground">Total Nodes</div>
            <div className="text-3xl font-mono font-bold text-primary">{totalNodes}</div>
          </div>
          <div className="space-y-1">
            <div className="text-sm text-muted-foreground">Fleet Health</div>
            <div className="text-3xl font-mono font-bold text-primary">{healthPercentage.toFixed(0)}%</div>
          </div>
        </div>

        <div className="space-y-3">
          <div className="text-sm font-medium text-muted-foreground">Status Distribution</div>
          <div className="grid grid-cols-2 gap-3">
            {['online', 'degraded', 'offline', 'provisioning'].map((status) => {
              const count = statusCounts[status] || 0
              const Icon = getStatusIcon(status)
              return (
                <div key={status} className="flex items-center gap-2 rounded-lg border border-border/50 bg-muted/20 p-3">
                  <Icon className={getStatusColor(status)} />
                  <div className="flex-1">
                    <div className="text-xs capitalize text-muted-foreground">{status}</div>
                    <div className="text-lg font-mono font-semibold">{count}</div>
                  </div>
                </div>
              )
            })}
          </div>
        </div>

        <div className="space-y-3">
          <div className="text-sm font-medium text-muted-foreground">Average Resource Usage</div>
          <div className="space-y-3">
            <div className="space-y-1.5">
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">CPU</span>
                <span className="font-mono font-medium">{avgCpu.toFixed(1)}%</span>
              </div>
              <Progress value={avgCpu} className="h-2" />
            </div>
            <div className="space-y-1.5">
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Memory</span>
                <span className="font-mono font-medium">{avgMemory.toFixed(1)}%</span>
              </div>
              <Progress value={avgMemory} className="h-2" />
            </div>
            <div className="space-y-1.5">
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Storage</span>
                <span className="font-mono font-medium">{avgStorage.toFixed(1)}%</span>
              </div>
              <Progress value={avgStorage} className="h-2" />
            </div>
          </div>
        </div>

        <div className="grid grid-cols-2 gap-4 pt-2">
          <div className="space-y-1 rounded-lg border border-border/50 bg-muted/20 p-3">
            <div className="text-xs text-muted-foreground">Zones</div>
            <div className="text-2xl font-mono font-bold text-primary">{zones.length}</div>
          </div>
          <div className="space-y-1 rounded-lg border border-border/50 bg-muted/20 p-3">
            <div className="text-xs text-muted-foreground">Racks</div>
            <div className="text-2xl font-mono font-bold text-primary">{racks.length}</div>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
