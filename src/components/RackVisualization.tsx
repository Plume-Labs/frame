import { useMemo } from 'react'
import { ClusterNode, RackPowerCooling } from '@/lib/types'
import { organizeNodesByRack, organizeRacksFlat, calculateRackPowerCooling } from '@/lib/rack'
import { RackView } from './RackView'
import { RackPowerCoolingCard } from './RackPowerCoolingCard'
import { RackLegend } from './RackLegend'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Stack } from '@phosphor-icons/react'

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
  const { racksMap, racks, rackPowerCooling } = useMemo(() => {
    const racksMap = organizeNodesByRack(nodes)
    const racks = organizeRacksFlat(racksMap)

    const rackPowerCooling = new Map<string, RackPowerCooling>()
    racksMap.forEach((rack) => {
      rackPowerCooling.set(rack.id, calculateRackPowerCooling(rack))
    })

    return { racksMap, racks, rackPowerCooling }
  }, [nodes])

  const totalNodes = racks.reduce((sum, rack) => sum + rack.nodes.length, 0)
  const onlineNodes = racks.reduce(
    (sum, rack) => sum + rack.nodes.filter((n) => n.status === 'online').length,
    0,
  )
  const avgHealth = racks.length
    ? racks.reduce((sum, rack) => sum + rack.healthScore, 0) / racks.length
    : 0
  const totalPowerDraw = racks.reduce(
    (sum, rack) => sum + (rackPowerCooling.get(rack.id)?.power.currentDraw ?? 0),
    0,
  )

  const healthColor =
    avgHealth >= 80
      ? 'bg-primary/20 text-primary border-primary'
      : avgHealth >= 60
        ? 'bg-warning/20 text-warning border-warning'
        : 'bg-destructive/20 text-destructive border-destructive'

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
        </div>
      </div>

      {selectedRackData && selectedRackPowerCooling && onSelectRack && (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-mono text-muted-foreground">
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

      <Card className="border-2">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-3">
              <Stack className="w-6 h-6 text-primary" weight="duotone" />
              <div>
                <CardTitle className="text-xl font-mono">Racks</CardTitle>
                <p className="text-sm text-muted-foreground font-mono mt-1">
                  {racks.length} racks · {totalNodes} nodes · {onlineNodes} online · {Math.round(totalPowerDraw / 1000)}kW
                </p>
              </div>
            </div>
            <Badge variant="outline" className={healthColor}>
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
    </div>
  )
}
