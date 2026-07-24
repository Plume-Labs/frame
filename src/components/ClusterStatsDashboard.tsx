import { ClusterStats } from '@/lib/types'

interface ClusterStatsProps {
  stats: ClusterStats
  /** Retained for call-site compatibility — the dashboard only renders the compact bar. */
  compact?: boolean
}

export function ClusterStatsDashboard({ stats }: ClusterStatsProps) {
  const cpuPercent = (stats.usedCpu / stats.totalCpu) * 100
  const memoryPercent = (stats.usedMemory / stats.totalMemory) * 100
  const storagePercent = (stats.usedStorage / stats.totalStorage) * 100

  return (
    <div className="flex items-center gap-4 font-mono text-sm">
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground">Nodes:</span>
        <span className="text-primary font-semibold">{stats.totalNodes}</span>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-accent font-semibold">{stats.onlineNodes}</span>
        <span className="text-muted-foreground text-xs">online</span>
      </div>
      {stats.degradedNodes > 0 && (
        <div className="flex items-center gap-2">
          <span className="text-warning font-semibold">{stats.degradedNodes}</span>
          <span className="text-muted-foreground text-xs">degraded</span>
        </div>
      )}
      {stats.offlineNodes > 0 && (
        <div className="flex items-center gap-2">
          <span className="text-destructive font-semibold">{stats.offlineNodes}</span>
          <span className="text-muted-foreground text-xs">offline</span>
        </div>
      )}
      <div className="h-4 w-px bg-border mx-2" />
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground">CPU:</span>
        <span className="font-semibold">{cpuPercent.toFixed(0)}%</span>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground">Memory:</span>
        <span className="font-semibold">{memoryPercent.toFixed(0)}%</span>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground">Storage:</span>
        <span className="font-semibold">{storagePercent.toFixed(0)}%</span>
      </div>
    </div>
  )
}
