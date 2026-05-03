import { ClusterStats } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { formatBytes, formatBandwidth } from '@/lib/cluster'
import { Cpu, HardDrives, Database, Network } from '@phosphor-icons/react'

interface ClusterStatsProps {
  stats: ClusterStats
}

export function ClusterStatsDashboard({ stats }: ClusterStatsProps) {
  const cpuPercent = (stats.usedCpu / stats.totalCpu) * 100
  const memoryPercent = (stats.usedMemory / stats.totalMemory) * 100
  const storagePercent = (stats.usedStorage / stats.totalStorage) * 100

  const metrics = [
    {
      icon: Cpu,
      label: 'CPU',
      used: stats.usedCpu.toFixed(1),
      total: stats.totalCpu,
      percent: cpuPercent,
      unit: 'cores'
    },
    {
      icon: HardDrives,
      label: 'Memory',
      used: formatBytes(stats.usedMemory),
      total: formatBytes(stats.totalMemory),
      percent: memoryPercent,
      unit: ''
    },
    {
      icon: Database,
      label: 'Storage',
      used: formatBytes(stats.usedStorage),
      total: formatBytes(stats.totalStorage),
      percent: storagePercent,
      unit: ''
    },
    {
      icon: Network,
      label: 'Network',
      used: formatBandwidth(stats.networkThroughput),
      total: '',
      percent: 0,
      unit: 'throughput'
    }
  ]

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl">Cluster Overview</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="space-y-1">
              <div className="text-sm text-muted-foreground">Total Nodes</div>
              <div className="text-3xl font-mono font-bold text-primary">
                {stats.totalNodes}
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-sm text-muted-foreground">Online</div>
              <div className="text-3xl font-mono font-bold text-accent">
                {stats.onlineNodes}
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-sm text-muted-foreground">Degraded</div>
              <div className="text-3xl font-mono font-bold text-[oklch(0.75_0.18_75)]">
                {stats.degradedNodes}
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-sm text-muted-foreground">Offline</div>
              <div className="text-3xl font-mono font-bold text-destructive">
                {stats.offlineNodes}
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl">Resource Allocation</CardTitle>
        </CardHeader>
        <CardContent className="space-y-6">
          {metrics.map((metric) => {
            const Icon = metric.icon
            return (
              <div key={metric.label} className="space-y-2">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <Icon className="text-primary text-xl" />
                    <span className="font-medium">{metric.label}</span>
                  </div>
                  <div className="font-mono text-sm">
                    {metric.total ? (
                      <>
                        <span className="text-foreground font-semibold">{metric.used}</span>
                        <span className="text-muted-foreground"> / {metric.total}</span>
                        {metric.unit && <span className="text-muted-foreground"> {metric.unit}</span>}
                      </>
                    ) : (
                      <span className="text-foreground font-semibold">
                        {metric.used}
                      </span>
                    )}
                  </div>
                </div>
                {metric.total && (
                  <div className="space-y-1">
                    <Progress value={metric.percent} className="h-2" />
                    <div className="text-xs text-muted-foreground text-right font-mono">
                      {metric.percent.toFixed(1)}%
                    </div>
                  </div>
                )}
              </div>
            )
          })}
        </CardContent>
      </Card>
    </div>
  )
}
