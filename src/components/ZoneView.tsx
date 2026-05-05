import { useMemo, useState, useEffect } from 'react'
import { ClusterNode, RackPowerCooling } from '@/lib/types'
import { organizeNodesByRack, organizeRacksByZone, calculateRackPowerCooling, RackData } from '@/lib/rack'
import { RackView } from './RackView'
import { RackPowerCoolingCard } from './RackPowerCoolingCard'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { 
  Buildings, 
  Lightning, 
  Snowflake, 
  HardDrive, 
  ChartLine,
  XCircle
} from '@phosphor-icons/react'

interface ZoneViewProps {
  nodes: ClusterNode[]
  selectedNode: ClusterNode | null
  onSelectNode: (node: ClusterNode) => void
  initialZone?: string | null
  onZoneConsumed?: () => void
}

interface ZoneStats {
  zoneName: string
  racks: RackData[]
  totalNodes: number
  onlineNodes: number
  degradedNodes: number
  offlineNodes: number
  avgHealth: number
  totalPowerDraw: number
  maxPowerCapacity: number
  avgTemperature: number
  totalStorage: number
  usedStorage: number
  criticalAlerts: number
  warningAlerts: number
}

export function ZoneView({ nodes, selectedNode, onSelectNode, initialZone, onZoneConsumed }: ZoneViewProps) {
  const [selectedZone, setSelectedZone] = useState<string | null>(initialZone ?? null)
  const [selectedRack, setSelectedRack] = useState<string | null>(null)

  // When a zone is selected externally (e.g. from the heatmap), navigate to it
  useEffect(() => {
    if (initialZone) {
      setSelectedZone(initialZone)
      setSelectedRack(null)
      onZoneConsumed?.()
    }
  }, [initialZone, onZoneConsumed])

  const { zoneStats, racksMap, rackPowerCooling } = useMemo(() => {
    const racksMap = organizeNodesByRack(nodes)
    const zoneMap = organizeRacksByZone(racksMap)
    
    const rackPowerCooling = new Map<string, RackPowerCooling>()
    racksMap.forEach((rack) => {
      rackPowerCooling.set(rack.id, calculateRackPowerCooling(rack))
    })
    
    const zoneStats: ZoneStats[] = Array.from(zoneMap.entries()).map(([zoneName, racks]) => {
      const totalNodes = racks.reduce((sum, rack) => sum + rack.nodes.length, 0)
      const onlineNodes = racks.reduce((sum, rack) => 
        sum + rack.nodes.filter(n => n.status === 'online').length, 0
      )
      const degradedNodes = racks.reduce((sum, rack) => 
        sum + rack.nodes.filter(n => n.status === 'degraded').length, 0
      )
      const offlineNodes = racks.reduce((sum, rack) => 
        sum + rack.nodes.filter(n => n.status === 'offline').length, 0
      )
      const avgHealth = racks.reduce((sum, rack) => sum + rack.healthScore, 0) / racks.length

      const totalPowerDraw = racks.reduce((sum, rack) => {
        const pc = rackPowerCooling.get(rack.id)
        return sum + (pc?.power.currentDraw || 0)
      }, 0)

      const maxPowerCapacity = racks.reduce((sum, rack) => {
        const pc = rackPowerCooling.get(rack.id)
        return sum + (pc?.power.maxCapacity || 0)
      }, 0)

      const allNodes = racks.flatMap(r => r.nodes)
      const avgTemperature = allNodes.reduce((sum, n) => sum + n.hardware.temperature, 0) / allNodes.length

      const totalStorage = racks.reduce((sum, rack) => sum + rack.totalCapacity, 0)
      const usedStorage = racks.reduce((sum, rack) => sum + rack.usedCapacity, 0)

      const criticalAlerts = racks.reduce((sum, rack) => {
        const pc = rackPowerCooling.get(rack.id)
        return sum + (pc?.alerts.filter(a => a.severity === 'critical').length || 0)
      }, 0)

      const warningAlerts = racks.reduce((sum, rack) => {
        const pc = rackPowerCooling.get(rack.id)
        return sum + (pc?.alerts.filter(a => a.severity === 'warning').length || 0)
      }, 0)

      return {
        zoneName,
        racks,
        totalNodes,
        onlineNodes,
        degradedNodes,
        offlineNodes,
        avgHealth,
        totalPowerDraw,
        maxPowerCapacity,
        avgTemperature,
        totalStorage,
        usedStorage,
        criticalAlerts,
        warningAlerts
      }
    })
    
    return { zoneStats, racksMap, rackPowerCooling }
  }, [nodes])

  const selectedZoneData = selectedZone 
    ? zoneStats.find(z => z.zoneName === selectedZone)
    : null

  const selectedRackData = selectedRack ? racksMap.get(selectedRack) : null
  const selectedRackPowerCooling = selectedRack ? rackPowerCooling.get(selectedRack) : null

  const getHealthColor = (health: number) => {
    if (health >= 80) return 'text-primary'
    if (health >= 60) return 'text-warning'
    return 'text-destructive'
  }

  const getHealthBgColor = (health: number) => {
    if (health >= 80) return 'bg-primary/20 text-primary border-primary'
    if (health >= 60) return 'bg-warning/20 text-warning border-warning'
    return 'bg-destructive/20 text-destructive border-destructive'
  }

  if (selectedZoneData && selectedRackData && selectedRackPowerCooling) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-4">
          <Button
            variant="ghost"
            onClick={() => {
              setSelectedRack(null)
              setSelectedZone(null)
            }}
            className="font-mono"
          >
            ← Back to Zones
          </Button>
          <div>
            <h2 className="text-2xl font-mono font-bold text-primary">
              {selectedRackData.id}
            </h2>
            <p className="text-sm text-muted-foreground font-mono">
              {selectedZoneData.zoneName} · {selectedRackData.nodes.length} nodes
            </p>
          </div>
        </div>

        <RackPowerCoolingCard metrics={selectedRackPowerCooling} />

        <Card>
          <CardHeader>
            <CardTitle className="font-mono">Rack Layout</CardTitle>
          </CardHeader>
          <CardContent className="flex justify-center">
            <RackView
              rack={selectedRackData}
              powerCooling={selectedRackPowerCooling}
              selectedNode={selectedNode}
              onSelectNode={onSelectNode}
            />
          </CardContent>
        </Card>
      </div>
    )
  }

  if (selectedZoneData) {
    return (
      <div className="space-y-6">
        <div className="flex items-center gap-4">
          <Button
            variant="ghost"
            onClick={() => setSelectedZone(null)}
            className="font-mono"
          >
            ← Back to Zones
          </Button>
          <div>
            <h2 className="text-2xl font-mono font-bold text-primary uppercase">
              {selectedZoneData.zoneName}
            </h2>
            <p className="text-sm text-muted-foreground font-mono">
              {selectedZoneData.racks.length} racks · {selectedZoneData.totalNodes} nodes
            </p>
          </div>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          <Card>
            <CardContent className="pt-6">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm text-muted-foreground font-mono">Health Score</p>
                  <p className={`text-3xl font-mono font-bold mt-1 ${getHealthColor(selectedZoneData.avgHealth)}`}>
                    {Math.round(selectedZoneData.avgHealth)}%
                  </p>
                </div>
                <ChartLine className="w-8 h-8 text-muted-foreground" weight="duotone" />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardContent className="pt-6">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm text-muted-foreground font-mono">Power Draw</p>
                  <p className="text-3xl font-mono font-bold mt-1">
                    {Math.round(selectedZoneData.totalPowerDraw / 1000)}kW
                  </p>
                  <p className="text-xs text-muted-foreground font-mono">
                    of {Math.round(selectedZoneData.maxPowerCapacity / 1000)}kW
                  </p>
                </div>
                <Lightning className="w-8 h-8 text-muted-foreground" weight="duotone" />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardContent className="pt-6">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm text-muted-foreground font-mono">Avg Temperature</p>
                  <p className="text-3xl font-mono font-bold mt-1">
                    {selectedZoneData.avgTemperature.toFixed(1)}°C
                  </p>
                </div>
                <Snowflake className="w-8 h-8 text-muted-foreground" weight="duotone" />
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardContent className="pt-6">
              <div className="flex items-center justify-between">
                <div>
                  <p className="text-sm text-muted-foreground font-mono">Storage</p>
                  <p className="text-3xl font-mono font-bold mt-1">
                    {Math.round((selectedZoneData.usedStorage / selectedZoneData.totalStorage) * 100)}%
                  </p>
                  <p className="text-xs text-muted-foreground font-mono">
                    {Math.round(selectedZoneData.usedStorage / 1024)}TB used
                  </p>
                </div>
                <HardDrive className="w-8 h-8 text-muted-foreground" weight="duotone" />
              </div>
            </CardContent>
          </Card>
        </div>

        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <Card className="border-primary/50">
            <CardContent className="pt-6 text-center">
              <p className="text-sm text-muted-foreground font-mono">Online</p>
              <p className="text-2xl font-mono font-bold text-primary mt-1">
                {selectedZoneData.onlineNodes}
              </p>
            </CardContent>
          </Card>

          <Card className="border-warning/50">
            <CardContent className="pt-6 text-center">
              <p className="text-sm text-muted-foreground font-mono">Degraded</p>
              <p className="text-2xl font-mono font-bold text-warning mt-1">
                {selectedZoneData.degradedNodes}
              </p>
            </CardContent>
          </Card>

          <Card className="border-destructive/50">
            <CardContent className="pt-6 text-center">
              <p className="text-sm text-muted-foreground font-mono">Offline</p>
              <p className="text-2xl font-mono font-bold text-destructive mt-1">
                {selectedZoneData.offlineNodes}
              </p>
            </CardContent>
          </Card>

          <Card className="border-muted">
            <CardContent className="pt-6 text-center">
              <p className="text-sm text-muted-foreground font-mono">Alerts</p>
              <p className="text-2xl font-mono font-bold mt-1">
                <span className="text-destructive">{selectedZoneData.criticalAlerts}</span>
                <span className="text-muted-foreground mx-1">/</span>
                <span className="text-warning">{selectedZoneData.warningAlerts}</span>
              </p>
            </CardContent>
          </Card>
        </div>

        {(selectedZoneData.criticalAlerts > 0 || selectedZoneData.warningAlerts > 0) && (
          <Card className="border-warning">
            <CardHeader>
              <CardTitle className="font-mono text-warning flex items-center gap-2">
                <XCircle weight="duotone" />
                Active Alerts
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="space-y-2">
                {selectedZoneData.racks.map(rack => {
                  const pc = rackPowerCooling.get(rack.id)
                  if (!pc || pc.alerts.length === 0) return null
                  
                  return (
                    <div key={rack.id} className="space-y-1">
                      <p className="text-sm font-mono font-semibold">{rack.id}</p>
                      {pc.alerts.map((alert, idx) => (
                        <div
                          key={idx}
                          className={`text-xs font-mono p-2 rounded border ${
                            alert.severity === 'critical'
                              ? 'bg-destructive/10 border-destructive text-destructive'
                              : 'bg-warning/10 border-warning text-warning'
                          }`}
                        >
                          {alert.message}
                        </div>
                      ))}
                    </div>
                  )
                })}
              </div>
            </CardContent>
          </Card>
        )}

        <Card>
          <CardHeader>
            <CardTitle className="font-mono">Racks in {selectedZoneData.zoneName}</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
              {selectedZoneData.racks.map((rack) => (
                <div 
                  key={rack.id}
                  onClick={() => setSelectedRack(rack.id)}
                  className="cursor-pointer transition-all hover:scale-105"
                >
                  <RackView
                    rack={rack}
                    powerCooling={rackPowerCooling.get(rack.id)}
                    selectedNode={selectedNode}
                    onSelectNode={onSelectNode}
                  />
                </div>
              ))}
            </div>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-2xl font-mono font-bold text-primary">Zone Overview</h2>
        <p className="text-sm text-muted-foreground font-mono mt-1">
          Click on a zone to explore its racks and nodes
        </p>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {zoneStats.map((zone) => {
          const powerUtilization = (zone.totalPowerDraw / zone.maxPowerCapacity) * 100
          const storageUtilization = (zone.usedStorage / zone.totalStorage) * 100

          return (
            <Card 
              key={zone.zoneName}
              className="border-2 hover:border-primary/50 transition-colors cursor-pointer"
              onClick={() => setSelectedZone(zone.zoneName)}
            >
              <CardHeader>
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    <Buildings className="w-8 h-8 text-primary" weight="duotone" />
                    <div>
                      <CardTitle className="text-xl font-mono uppercase">
                        {zone.zoneName}
                      </CardTitle>
                      <p className="text-sm text-muted-foreground font-mono mt-1">
                        {zone.racks.length} racks · {zone.totalNodes} nodes
                      </p>
                    </div>
                  </div>
                  <Badge variant="outline" className={getHealthBgColor(zone.avgHealth)}>
                    {Math.round(zone.avgHealth)}%
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="space-y-4">
                <div className="grid grid-cols-3 gap-4 text-center">
                  <div>
                    <p className="text-xs text-muted-foreground font-mono">Online</p>
                    <p className="text-xl font-mono font-bold text-primary">{zone.onlineNodes}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground font-mono">Degraded</p>
                    <p className="text-xl font-mono font-bold text-warning">{zone.degradedNodes}</p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground font-mono">Offline</p>
                    <p className="text-xl font-mono font-bold text-destructive">{zone.offlineNodes}</p>
                  </div>
                </div>

                <div className="space-y-3">
                  <div>
                    <div className="flex justify-between text-xs font-mono mb-1">
                      <span className="text-muted-foreground">Power</span>
                      <span>{Math.round(zone.totalPowerDraw / 1000)}kW / {Math.round(zone.maxPowerCapacity / 1000)}kW</span>
                    </div>
                    <div className="h-2 bg-secondary rounded-full overflow-hidden">
                      <div
                        className={`h-full transition-all ${
                          powerUtilization > 85 ? 'bg-destructive' :
                          powerUtilization > 75 ? 'bg-warning' : 'bg-primary'
                        }`}
                        style={{ width: `${Math.min(powerUtilization, 100)}%` }}
                      />
                    </div>
                  </div>

                  <div>
                    <div className="flex justify-between text-xs font-mono mb-1">
                      <span className="text-muted-foreground">Storage</span>
                      <span>{Math.round(zone.usedStorage / 1024)}TB / {Math.round(zone.totalStorage / 1024)}TB</span>
                    </div>
                    <div className="h-2 bg-secondary rounded-full overflow-hidden">
                      <div
                        className="h-full bg-primary transition-all"
                        style={{ width: `${Math.min(storageUtilization, 100)}%` }}
                      />
                    </div>
                  </div>

                  <div>
                    <div className="flex justify-between text-xs font-mono mb-1">
                      <span className="text-muted-foreground">Temperature</span>
                      <span>{zone.avgTemperature.toFixed(1)}°C</span>
                    </div>
                    <div className="h-2 bg-secondary rounded-full overflow-hidden">
                      <div
                        className={`h-full transition-all ${
                          zone.avgTemperature > 75 ? 'bg-destructive' :
                          zone.avgTemperature > 65 ? 'bg-warning' : 'bg-accent'
                        }`}
                        style={{ width: `${Math.min((zone.avgTemperature / 85) * 100, 100)}%` }}
                      />
                    </div>
                  </div>
                </div>

                {(zone.criticalAlerts > 0 || zone.warningAlerts > 0) && (
                  <div className="flex items-center gap-2 text-xs font-mono pt-2 border-t border-border">
                    <XCircle weight="duotone" className="text-warning" />
                    <span className="text-destructive font-semibold">{zone.criticalAlerts} Critical</span>
                    <span className="text-muted-foreground">·</span>
                    <span className="text-warning font-semibold">{zone.warningAlerts} Warning</span>
                  </div>
                )}
              </CardContent>
            </Card>
          )
        })}
      </div>
    </div>
  )
}
