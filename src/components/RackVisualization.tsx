import { useMemo } from 'react'
import { ClusterNode, RackPowerCooling } from '@/lib/types'
import { organizeNodesByRack, organizeRacksByZone, calculateRackPowerCooling } from '@/lib/rack'
import { RackView } from './RackView'
import { RackPowerCoolingCard } from './RackPowerCoolingCard'
import { RackConfigDialog } from './RackConfigDialog'
import { RackLegend } from './RackLegend'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Buildings } from '@phosphor-icons/react'
import { toast } from 'sonner'

interface RackVisualizationProps {
  nodes: ClusterNode[]
  selectedNode: ClusterNode | null
  onSelectNode: (node: ClusterNode) => void
  selectedRack?: string | null
  onSelectRack?: (rackId: string | null) => void
}

export function RackVisualization({ 
  nodes, 
  selectedNode, 
  onSelectNode,
  selectedRack,
  onSelectRack
}: RackVisualizationProps) {
  const { racksMap, zoneMap, rackPowerCooling } = useMemo(() => {
    const racksMap = organizeNodesByRack(nodes)
    const zoneMap = organizeRacksByZone(racksMap)
    
    const rackPowerCooling = new Map<string, RackPowerCooling>()
    racksMap.forEach((rack) => {
      rackPowerCooling.set(rack.id, calculateRackPowerCooling(rack))
    })
    
    return { racksMap, zoneMap, rackPowerCooling }
  }, [nodes])

  const selectedRackData = selectedRack ? racksMap.get(selectedRack) : null
  const selectedRackPowerCooling = selectedRack ? rackPowerCooling.get(selectedRack) : null

  return (
    <div className="space-y-6">
      <div className="flex justify-between items-center">
        <div className="text-sm text-muted-foreground font-mono">
          Rack visualization with unit markers and device type annotations
        </div>
        <div className="flex gap-2">
          <RackLegend />
          <RackConfigDialog
            onAddDevice={(config) => {
              toast.success(`Device ${config.name} configured for provisioning`, {
                description: `${config.deviceType} (${config.rackUnits}U) → ${config.rackId} @ U${config.rackPosition}`
              })
            }}
          />
        </div>
      </div>

      {selectedRackData && selectedRackPowerCooling && onSelectRack && (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <h3 className="text-xl font-mono font-semibold text-foreground">
              {selectedRackData.id} Details
            </h3>
            <button
              onClick={() => onSelectRack(null)}
              className="text-sm text-muted-foreground hover:text-foreground font-mono"
            >
              Close
            </button>
          </div>
          <RackPowerCoolingCard metrics={selectedRackPowerCooling} />
        </div>
      )}

      {Array.from(zoneMap.entries()).map(([zoneName, racks]) => {
        const totalNodes = racks.reduce((sum, rack) => sum + rack.nodes.length, 0)
        const onlineNodes = racks.reduce((sum, rack) => 
          sum + rack.nodes.filter(n => n.status === 'online').length, 0
        )
        const avgHealth = racks.reduce((sum, rack) => sum + rack.healthScore, 0) / racks.length

        const totalPowerDraw = racks.reduce((sum, rack) => {
          const pc = rackPowerCooling.get(rack.id)
          return sum + (pc?.power.currentDraw || 0)
        }, 0)

        const getZoneHealthColor = () => {
          if (avgHealth >= 80) return 'bg-primary/20 text-primary border-primary'
          if (avgHealth >= 60) return 'bg-warning/20 text-warning border-warning'
          return 'bg-destructive/20 text-destructive border-destructive'
        }

        return (
          <Card key={zoneName} className="border-2">
            <CardHeader>
              <div className="flex flex-wrap items-center justify-between gap-4">
                <div className="flex items-center gap-3">
                  <Buildings className="w-6 h-6 text-primary" weight="duotone" />
                  <div>
                    <CardTitle className="text-xl font-mono uppercase">{zoneName}</CardTitle>
                    <p className="text-sm text-muted-foreground font-mono mt-1">
                      {racks.length} racks · {totalNodes} nodes · {onlineNodes} online · {Math.round(totalPowerDraw / 1000)}kW
                    </p>
                  </div>
                </div>
                <Badge variant="outline" className={getZoneHealthColor()}>
                  Health: {Math.round(avgHealth)}%
                </Badge>
              </div>
            </CardHeader>
            <CardContent>
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
                {racks.map((rack) => (
                  <div 
                    key={rack.id}
                    onClick={() => onSelectRack?.(rack.id)}
                    className={`cursor-pointer transition-all ${
                      selectedRack === rack.id ? 'ring-2 ring-primary rounded-lg' : ''
                    }`}
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
        )
      })}
    </div>
  )
}
