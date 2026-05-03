import { ClusterNode } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Database, HardDrives, ArrowsClockwise } from '@phosphor-icons/react'
import { formatBytes } from '@/lib/cluster'

interface StorageDashboardProps {
  nodes: ClusterNode[]
}

export function StorageDashboard({ nodes }: StorageDashboardProps) {
  const activeNodes = nodes.filter(n => n.status !== 'offline')
  
  const totalOSDs = activeNodes.reduce((sum, n) => sum + n.storage.cephOSDs, 0)
  const totalPGs = activeNodes.reduce((sum, n) => sum + n.storage.cephPGs, 0)
  const totalCapacity = activeNodes.reduce((sum, n) => sum + n.storage.totalCapacity, 0)
  const totalUsed = activeNodes.reduce((sum, n) => sum + n.storage.usedCapacity, 0)
  const totalReadIOPS = activeNodes.reduce((sum, n) => sum + n.storage.readIOPS, 0)
  const totalWriteIOPS = activeNodes.reduce((sum, n) => sum + n.storage.writeIOPS, 0)
  const avgReplication = activeNodes.reduce((sum, n) => sum + n.storage.replicationFactor, 0) / activeNodes.length

  const usagePercent = (totalUsed / totalCapacity) * 100
  const totalAvailable = totalCapacity - totalUsed

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-xl flex items-center gap-2">
          <Database className="text-primary" />
          Ceph Storage Cluster
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">Total OSDs</div>
            <div className="text-2xl font-mono font-bold text-primary">
              {totalOSDs}
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">Placement Groups</div>
            <div className="text-2xl font-mono font-bold text-accent">
              {totalPGs}
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">Replication</div>
            <div className="text-2xl font-mono font-bold text-foreground">
              {avgReplication.toFixed(0)}x
            </div>
          </div>
        </div>

        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <HardDrives className="text-primary" />
              <span className="font-medium">Capacity Usage</span>
            </div>
            <span className="font-mono text-sm">
              <span className="font-semibold text-foreground">{formatBytes(totalUsed)}</span>
              <span className="text-muted-foreground"> / {formatBytes(totalCapacity)}</span>
            </span>
          </div>
          <Progress value={usagePercent} className="h-3" />
          <div className="flex items-center justify-between text-xs">
            <span className={`font-mono font-semibold ${usagePercent > 80 ? 'text-[oklch(0.75_0.18_75)]' : 'text-foreground'}`}>
              {usagePercent.toFixed(1)}% used
            </span>
            <span className="font-mono text-muted-foreground">
              {formatBytes(totalAvailable)} available
            </span>
          </div>
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="p-4 rounded-lg bg-secondary/30 border border-border">
            <div className="flex items-center justify-between mb-2">
              <div className="flex items-center gap-2">
                <ArrowsClockwise className="text-accent" />
                <span className="text-sm font-medium">Read IOPS</span>
              </div>
            </div>
            <div className="font-mono text-2xl font-bold text-foreground">
              {totalReadIOPS.toLocaleString()}
            </div>
            <div className="text-xs text-muted-foreground mt-1">
              Avg: {(totalReadIOPS / activeNodes.length).toFixed(0)} per node
            </div>
          </div>
          
          <div className="p-4 rounded-lg bg-secondary/30 border border-border">
            <div className="flex items-center justify-between mb-2">
              <div className="flex items-center gap-2">
                <ArrowsClockwise className="text-primary" />
                <span className="text-sm font-medium">Write IOPS</span>
              </div>
            </div>
            <div className="font-mono text-2xl font-bold text-foreground">
              {totalWriteIOPS.toLocaleString()}
            </div>
            <div className="text-xs text-muted-foreground mt-1">
              Avg: {(totalWriteIOPS / activeNodes.length).toFixed(0)} per node
            </div>
          </div>
        </div>

        {usagePercent > 80 && (
          <div className="p-4 rounded-lg bg-[oklch(0.75_0.18_75)]/10 border border-[oklch(0.75_0.18_75)]/30">
            <div className="text-sm font-semibold text-[oklch(0.75_0.18_75)]">
              Storage Capacity Warning
            </div>
            <div className="text-xs text-muted-foreground mt-1">
              Cluster storage usage is above 80%. Consider adding more capacity or cleaning up data.
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
