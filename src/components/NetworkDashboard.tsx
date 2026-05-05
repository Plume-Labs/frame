import { ClusterNode } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Network, ArrowUp, ArrowDown, Clock, Warning } from '@phosphor-icons/react'

interface NetworkDashboardProps {
  nodes: ClusterNode[]
}

function formatBytes(bytes: number): string {
  if (bytes >= 1e12) return `${(bytes / 1e12).toFixed(2)} TB`
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(2)} GB`
  if (bytes >= 1e6) return `${(bytes / 1e6).toFixed(2)} MB`
  return `${(bytes / 1e3).toFixed(2)} KB`
}

export function NetworkDashboard({ nodes }: NetworkDashboardProps) {
  const activeNodes = nodes.filter(n => n.status !== 'offline' && n.network)
  
  const totalRx = activeNodes.reduce((sum, n) => sum + (n.network?.rxBytes || 0), 0)
  const totalTx = activeNodes.reduce((sum, n) => sum + (n.network?.txBytes || 0), 0)
  const avgLatency = activeNodes.length > 0 ? activeNodes.reduce((sum, n) => sum + (n.network?.latency || 0), 0) / activeNodes.length : 0
  const avgPacketLoss = activeNodes.length > 0 ? activeNodes.reduce((sum, n) => sum + (n.network?.packetLoss || 0), 0) / activeNodes.length : 0
  const rdmaNodes = activeNodes.filter(n => n.network?.rdmaActive).length
  const totalBandwidth = activeNodes.reduce((sum, n) => sum + (n.network?.bandwidth || 0), 0)
  const totalQueuePairs = activeNodes.reduce((sum, n) => sum + (n.network?.rdmaQueuePairs || 0), 0)

  const highLatencyNodes = activeNodes.filter(n => n.network && n.network.latency > 3)
  const highPacketLossNodes = activeNodes.filter(n => n.network && n.network.packetLoss > 0.3)

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-xl flex items-center gap-2">
          <Network className="text-primary" />
          Network Statistics
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="grid grid-cols-2 gap-4">
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">Total Bandwidth</div>
            <div className="text-2xl font-mono font-bold text-primary">
              {(totalBandwidth / 1000).toFixed(1)} Gbps
            </div>
          </div>
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">RDMA Nodes</div>
            <div className="text-2xl font-mono font-bold text-accent">
              {rdmaNodes} / {activeNodes.length}
            </div>
          </div>
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div className="p-4 rounded-lg bg-secondary/30 border border-border">
            <div className="flex items-center justify-between mb-2">
              <div className="flex items-center gap-2">
                <ArrowDown className="text-accent" />
                <span className="text-sm font-medium">Received</span>
              </div>
              <span className="font-mono text-lg font-semibold">{formatBytes(totalRx)}</span>
            </div>
          </div>
          
          <div className="p-4 rounded-lg bg-secondary/30 border border-border">
            <div className="flex items-center justify-between mb-2">
              <div className="flex items-center gap-2">
                <ArrowUp className="text-primary" />
                <span className="text-sm font-medium">Transmitted</span>
              </div>
              <span className="font-mono text-lg font-semibold">{formatBytes(totalTx)}</span>
            </div>
          </div>
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <Clock className="text-sm text-muted-foreground" />
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Avg Latency</div>
            </div>
            <div className={`text-xl font-mono font-bold ${avgLatency > 3 ? 'text-[oklch(0.75_0.18_75)]' : 'text-foreground'}`}>
              {avgLatency.toFixed(2)} ms
            </div>
          </div>
          
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">Packet Loss</div>
            <div className={`text-xl font-mono font-bold ${avgPacketLoss > 0.3 ? 'text-[oklch(0.75_0.18_75)]' : 'text-foreground'}`}>
              {avgPacketLoss.toFixed(3)}%
            </div>
          </div>
          
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">RDMA Queue Pairs</div>
            <div className="text-xl font-mono font-bold text-foreground">
              {totalQueuePairs}
            </div>
          </div>
        </div>

        {(highLatencyNodes.length > 0 || highPacketLossNodes.length > 0) && (
          <div className="p-4 rounded-lg bg-[oklch(0.75_0.18_75)]/10 border border-[oklch(0.75_0.18_75)]/30">
            <div className="flex items-start gap-2">
              <Warning className="text-[oklch(0.75_0.18_75)] mt-0.5" />
              <div className="space-y-2 flex-1">
                <div className="text-sm font-semibold text-[oklch(0.75_0.18_75)]">Network Issues Detected</div>
                {highLatencyNodes.length > 0 && (
                  <div className="text-xs text-muted-foreground">
                    {highLatencyNodes.length} node{highLatencyNodes.length > 1 ? 's' : ''} with high latency: {' '}
                    {highLatencyNodes.slice(0, 3).map(n => n.name).join(', ')}
                    {highLatencyNodes.length > 3 && ` +${highLatencyNodes.length - 3} more`}
                  </div>
                )}
                {highPacketLossNodes.length > 0 && (
                  <div className="text-xs text-muted-foreground">
                    {highPacketLossNodes.length} node{highPacketLossNodes.length > 1 ? 's' : ''} with packet loss: {' '}
                    {highPacketLossNodes.slice(0, 3).map(n => n.name).join(', ')}
                    {highPacketLossNodes.length > 3 && ` +${highPacketLossNodes.length - 3} more`}
                  </div>
                )}
              </div>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
