import { ClusterNode, GPUMetrics } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Cpu, Thermometer, Lightning, Warning } from '@phosphor-icons/react'

interface GPUMonitoringDashboardProps {
  nodes: ClusterNode[]
}

function getUtilColor(pct: number) {
  if (pct >= 90) return 'text-destructive'
  if (pct >= 70) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-foreground'
}

function getTempColor(c: number) {
  if (c >= 85) return 'text-destructive'
  if (c >= 70) return 'text-[oklch(0.75_0.18_75)]'
  return 'text-foreground'
}

function GPUTile({ nodeName, gpu }: { nodeName: string; gpu: GPUMetrics }) {
  const memPct = (gpu.memoryUsedGB / gpu.memoryTotalGB) * 100
  const hasECCErrors = gpu.eccErrors > 0

  return (
    <div className={`p-3 rounded-lg border ${hasECCErrors ? 'border-destructive/40 bg-destructive/5' : 'border-border bg-secondary/30'}`}>
      <div className="flex items-center justify-between mb-2">
        <div>
          <div className="font-mono text-xs font-bold">{nodeName}</div>
          <div className="font-mono text-[10px] text-muted-foreground">GPU {gpu.gpuIndex} · {gpu.model}</div>
        </div>
        {hasECCErrors && (
          <span title={`${gpu.eccErrors} ECC errors`}>
            <Warning size={14} className="text-destructive" />
          </span>
        )}
      </div>

      <div className="space-y-2">
        <div>
          <div className="flex items-center justify-between text-xs mb-0.5">
            <span className="text-muted-foreground">SM Util</span>
            <span className={`font-mono font-bold ${getUtilColor(gpu.utilizationPercent)}`}>{gpu.utilizationPercent.toFixed(0)}%</span>
          </div>
          <Progress value={gpu.utilizationPercent} className="h-1" />
        </div>
        <div>
          <div className="flex items-center justify-between text-xs mb-0.5">
            <span className="text-muted-foreground">VRAM</span>
            <span className="font-mono text-xs">{gpu.memoryUsedGB.toFixed(0)}/{gpu.memoryTotalGB} GB</span>
          </div>
          <Progress value={memPct} className="h-1" />
        </div>
        <div className="grid grid-cols-3 gap-1 text-[10px] text-muted-foreground">
          <div>
            <span className={`font-mono font-bold ${getTempColor(gpu.temperatureC)}`}>{gpu.temperatureC.toFixed(0)}°C</span>
            <div>Temp</div>
          </div>
          <div>
            <span className="font-mono font-bold text-foreground">{gpu.powerWatts.toFixed(0)}W</span>
            <div>Power</div>
          </div>
          <div>
            <span className="font-mono font-bold text-primary">{gpu.nvlinkBandwidthGBps.toFixed(0)}GB/s</span>
            <div>NVLink</div>
          </div>
        </div>
        {gpu.migEnabled && (
          <div className="text-[10px] font-mono text-accent">MIG: {gpu.migInstances} instances</div>
        )}
      </div>
    </div>
  )
}

export function GPUMonitoringDashboard({ nodes }: GPUMonitoringDashboardProps) {
  const gpuNodes = nodes.filter(n => n.gpuMetrics && n.gpuMetrics.length > 0)
  const allGPUs = gpuNodes.flatMap(n => n.gpuMetrics!.map(g => ({ gpu: g, node: n })))

  if (allGPUs.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Cpu className="text-primary" />
            GPU Monitoring
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="text-center py-8 text-muted-foreground font-mono">No GPU nodes detected</div>
        </CardContent>
      </Card>
    )
  }

  const avgUtil = allGPUs.reduce((s, { gpu }) => s + gpu.utilizationPercent, 0) / allGPUs.length
  const totalPower = allGPUs.reduce((s, { gpu }) => s + gpu.powerWatts, 0)
  const avgTemp = allGPUs.reduce((s, { gpu }) => s + gpu.temperatureC, 0) / allGPUs.length
  const totalECC = allGPUs.reduce((s, { gpu }) => s + gpu.eccErrors, 0)
  const migEnabledGPUs = allGPUs.filter(({ gpu }) => gpu.migEnabled).length

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Cpu className="text-primary" />
            GPU Monitoring — {allGPUs.length} GPU{allGPUs.length !== 1 ? 's' : ''} across {gpuNodes.length} nodes
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Avg Utilization</div>
              <div className={`font-mono text-2xl font-bold ${getUtilColor(avgUtil)}`}>{avgUtil.toFixed(0)}%</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Total Power</div>
              <div className="font-mono text-2xl font-bold text-primary">
                <Lightning className="inline text-primary" size={20} /> {(totalPower / 1000).toFixed(1)} kW
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Avg Temp</div>
              <div className={`font-mono text-2xl font-bold flex items-center gap-1 ${getTempColor(avgTemp)}`}>
                <Thermometer size={20} /> {avgTemp.toFixed(0)}°C
              </div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">ECC Errors</div>
              <div className={`font-mono text-2xl font-bold ${totalECC > 0 ? 'text-destructive' : 'text-foreground'}`}>{totalECC}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">MIG-enabled GPUs</div>
              <div className="font-mono text-2xl font-bold text-accent">{migEnabledGPUs}</div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="text-xs text-muted-foreground uppercase tracking-wide">Cluster GPU Utilization</div>
            <Progress value={avgUtil} className="h-3" />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-lg">Per-GPU Heatmap</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3">
            {allGPUs.map(({ gpu, node }) => (
              <GPUTile key={`${node.id}-${gpu.gpuIndex}`} nodeName={node.name} gpu={gpu} />
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
