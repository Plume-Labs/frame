import { useMemo } from 'react'
import { ClusterNode } from '@/lib/types'
import { organizeNodesByRack, organizeRacksByZone } from '@/lib/rack'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Cpu, Database, HardDrive, WifiHigh, Thermometer } from '@phosphor-icons/react'

interface ZoneHeatmapProps {
  nodes: ClusterNode[]
  onZoneClick?: (zone: string) => void
}

interface ZoneMetrics {
  zoneName: string
  rackCount: number
  nodeCount: number
  cpuUsage: number
  memoryUsage: number
  storageUsage: number
  networkUsage: number
  temperature: number
}

interface RackMetrics {
  rackId: string
  zoneName: string
  nodeCount: number
  cpuUsage: number
  memoryUsage: number
  storageUsage: number
  networkUsage: number
  temperature: number
}

export function ZoneHeatmap({ nodes, onZoneClick }: ZoneHeatmapProps) {
  const { zoneMetrics, rackMetrics, globalMetrics } = useMemo(() => {
    const racksMap = organizeNodesByRack(nodes)
    const zoneMap = organizeRacksByZone(racksMap)

    const rackMetrics: RackMetrics[] = []
    racksMap.forEach((rack) => {
      const activeNodes = rack.nodes.filter(n => n.status === 'online' || n.status === 'degraded')
      if (activeNodes.length === 0) return

      const avgCpu = activeNodes.reduce((sum, n) => sum + n.metrics.cpu, 0) / activeNodes.length
      const avgMemory = activeNodes.reduce((sum, n) => sum + n.metrics.memory, 0) / activeNodes.length
      const avgStorage = activeNodes.reduce((sum, n) => sum + n.metrics.storage, 0) / activeNodes.length
      const avgNetwork = activeNodes.reduce((sum, n) => sum + n.metrics.network, 0) / activeNodes.length
      const avgTemp = activeNodes.reduce((sum, n) => sum + n.hardware.temperature, 0) / activeNodes.length

      rackMetrics.push({
        rackId: rack.id,
        zoneName: rack.zone,
        nodeCount: activeNodes.length,
        cpuUsage: avgCpu,
        memoryUsage: avgMemory,
        storageUsage: avgStorage,
        networkUsage: avgNetwork,
        temperature: avgTemp
      })
    })

    const zoneMetrics: ZoneMetrics[] = Array.from(zoneMap.entries()).map(([zoneName, racks]) => {
      const allNodes = racks.flatMap(r => r.nodes)
      const activeNodes = allNodes.filter(n => n.status === 'online' || n.status === 'degraded')

      if (activeNodes.length === 0) {
        return {
          zoneName,
          rackCount: racks.length,
          nodeCount: 0,
          cpuUsage: 0,
          memoryUsage: 0,
          storageUsage: 0,
          networkUsage: 0,
          temperature: 0
        }
      }

      return {
        zoneName,
        rackCount: racks.length,
        nodeCount: activeNodes.length,
        cpuUsage: activeNodes.reduce((sum, n) => sum + n.metrics.cpu, 0) / activeNodes.length,
        memoryUsage: activeNodes.reduce((sum, n) => sum + n.metrics.memory, 0) / activeNodes.length,
        storageUsage: activeNodes.reduce((sum, n) => sum + n.metrics.storage, 0) / activeNodes.length,
        networkUsage: activeNodes.reduce((sum, n) => sum + n.metrics.network, 0) / activeNodes.length,
        temperature: activeNodes.reduce((sum, n) => sum + n.hardware.temperature, 0) / activeNodes.length
      }
    })

    const activeNodes = nodes.filter(n => n.status === 'online' || n.status === 'degraded')
    const globalMetrics = activeNodes.length > 0 ? {
      cpu: activeNodes.reduce((sum, n) => sum + n.metrics.cpu, 0) / activeNodes.length,
      memory: activeNodes.reduce((sum, n) => sum + n.metrics.memory, 0) / activeNodes.length,
      storage: activeNodes.reduce((sum, n) => sum + n.metrics.storage, 0) / activeNodes.length,
      network: activeNodes.reduce((sum, n) => sum + n.metrics.network, 0) / activeNodes.length,
      temperature: activeNodes.reduce((sum, n) => sum + n.hardware.temperature, 0) / activeNodes.length
    } : {
      cpu: 0, memory: 0, storage: 0, network: 0, temperature: 0
    }

    return { zoneMetrics, rackMetrics, globalMetrics }
  }, [nodes])

  const getHeatBgStyle = (value: number, metric: 'cpu' | 'memory' | 'storage' | 'network' | 'temperature') => {
    let normalizedValue = value

    if (metric === 'temperature') {
      normalizedValue = Math.max(0, Math.min(100, ((value - 40) / 45) * 100))
    }

    const hue = 145 - (normalizedValue * 1.45)
    const lightness = 75 - (normalizedValue * 0.35)
    const saturation = 40 + (normalizedValue * 0.4)
    
    return {
      backgroundColor: `oklch(${lightness}% ${saturation}% ${hue})`,
      color: normalizedValue > 70 ? '#fff' : '#000'
    }
  }

  const renderHeatmapCell = (
    label: string,
    value: number,
    metric: 'cpu' | 'memory' | 'storage' | 'network' | 'temperature',
    onClick?: () => void
  ) => {
    const displayValue = metric === 'temperature' ? `${value.toFixed(1)}°C` : `${Math.round(value)}%`
    
    return (
      <div
        className={`
          p-4 rounded-lg border-2 transition-all
          ${onClick ? 'cursor-pointer hover:scale-105 hover:shadow-lg' : ''}
        `}
        style={getHeatBgStyle(value, metric)}
        onClick={onClick}
      >
        <div className="font-mono text-xs font-semibold mb-1 opacity-80">
          {label}
        </div>
        <div className="font-mono text-2xl font-bold">
          {displayValue}
        </div>
      </div>
    )
  }

  const renderMetricTab = (metric: 'cpu' | 'memory' | 'storage' | 'network' | 'temperature') => {
    const metricLabel = metric === 'cpu' ? 'CPU' : 
                       metric === 'memory' ? 'Memory' : 
                       metric === 'storage' ? 'Storage' : 
                       metric === 'network' ? 'Network' : 'Temperature'

    return (
      <div className="space-y-6">
        <Card className="border-primary/30">
          <CardHeader>
            <CardTitle className="font-mono text-lg">Cluster Average</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="max-w-xs">
              {renderHeatmapCell(
                `Global ${metricLabel}`,
                metric === 'temperature' ? globalMetrics.temperature : globalMetrics[metric],
                metric
              )}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="font-mono">Zone Heatmap - {metricLabel}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-4">
              {zoneMetrics.map((zone) => (
                <div key={zone.zoneName}>
                  {renderHeatmapCell(
                    zone.zoneName,
                    metric === 'temperature' ? zone.temperature : zone[`${metric}Usage`],
                    metric,
                    onZoneClick ? () => onZoneClick(zone.zoneName) : undefined
                  )}
                  <div className="mt-2 text-center">
                    <Badge variant="outline" className="font-mono text-xs">
                      {zone.rackCount} racks · {zone.nodeCount} nodes
                    </Badge>
                  </div>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="font-mono">Rack Heatmap - {metricLabel}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-6">
              {zoneMetrics.map((zone) => {
                const zoneRacks = rackMetrics.filter(r => r.zoneName === zone.zoneName)
                if (zoneRacks.length === 0) return null

                return (
                  <div key={zone.zoneName}>
                    <h3 className="font-mono font-semibold text-sm text-muted-foreground mb-3 uppercase">
                      {zone.zoneName}
                    </h3>
                    <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6 xl:grid-cols-8 gap-3">
                      {zoneRacks.map((rack) => (
                        <div key={rack.rackId}>
                          {renderHeatmapCell(
                            rack.rackId.split('-').pop() || rack.rackId,
                            metric === 'temperature' ? rack.temperature : rack[`${metric}Usage`],
                            metric
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                )
              })}
            </div>
          </CardContent>
        </Card>

        <Card className="bg-muted/30">
          <CardHeader>
            <CardTitle className="font-mono text-sm">Color Legend</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex flex-wrap gap-3 items-center">
              <div className="flex items-center gap-2">
                <div className="w-12 h-8 rounded border-2" style={getHeatBgStyle(10, metric)} />
                <span className="text-xs font-mono">Low</span>
              </div>
              <div className="flex items-center gap-2">
                <div className="w-12 h-8 rounded border-2" style={getHeatBgStyle(30, metric)} />
                <span className="text-xs font-mono">Moderate</span>
              </div>
              <div className="flex items-center gap-2">
                <div className="w-12 h-8 rounded border-2" style={getHeatBgStyle(50, metric)} />
                <span className="text-xs font-mono">Normal</span>
              </div>
              <div className="flex items-center gap-2">
                <div className="w-12 h-8 rounded border-2" style={getHeatBgStyle(70, metric)} />
                <span className="text-xs font-mono">High</span>
              </div>
              <div className="flex items-center gap-2">
                <div className="w-12 h-8 rounded border-2" style={getHeatBgStyle(90, metric)} />
                <span className="text-xs font-mono">Critical</span>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <Tabs defaultValue="cpu" className="w-full">
      <TabsList className="font-mono">
        <TabsTrigger value="cpu" className="gap-2">
          <Cpu weight="duotone" />
          CPU
        </TabsTrigger>
        <TabsTrigger value="memory" className="gap-2">
          <Database weight="duotone" />
          Memory
        </TabsTrigger>
        <TabsTrigger value="storage" className="gap-2">
          <HardDrive weight="duotone" />
          Storage
        </TabsTrigger>
        <TabsTrigger value="network" className="gap-2">
          <WifiHigh weight="duotone" />
          Network
        </TabsTrigger>
        <TabsTrigger value="temperature" className="gap-2">
          <Thermometer weight="duotone" />
          Temperature
        </TabsTrigger>
      </TabsList>

      <TabsContent value="cpu" className="mt-6">
        {renderMetricTab('cpu')}
      </TabsContent>

      <TabsContent value="memory" className="mt-6">
        {renderMetricTab('memory')}
      </TabsContent>

      <TabsContent value="storage" className="mt-6">
        {renderMetricTab('storage')}
      </TabsContent>

      <TabsContent value="network" className="mt-6">
        {renderMetricTab('network')}
      </TabsContent>

      <TabsContent value="temperature" className="mt-6">
        {renderMetricTab('temperature')}
      </TabsContent>
    </Tabs>
  )
}
