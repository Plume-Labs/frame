import { useMemo } from 'react'
import { ClusterNode } from '@/lib/types'
import { organizeNodesByRack, organizeRacksByZone } from '@/lib/rack'
import { RackView } from './RackView'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Buildings } from '@phosphor-icons/react'

interface RackVisualizationProps {
  nodes: ClusterNode[]
  selectedNode: ClusterNode | null
  onSelectNode: (node: ClusterNode) => void
}

export function RackVisualization({ nodes, selectedNode, onSelectNode }: RackVisualizationProps) {
  const { racksMap, zoneMap } = useMemo(() => {
    const racksMap = organizeNodesByRack(nodes)
    const zoneMap = organizeRacksByZone(racksMap)
    return { racksMap, zoneMap }
  }, [nodes])

  return (
    <div className="space-y-6">
      {Array.from(zoneMap.entries()).map(([zoneName, racks]) => {
        const totalNodes = racks.reduce((sum, rack) => sum + rack.nodes.length, 0)
        const onlineNodes = racks.reduce((sum, rack) => 
          sum + rack.nodes.filter(n => n.status === 'online').length, 0
        )
        const avgHealth = racks.reduce((sum, rack) => sum + rack.healthScore, 0) / racks.length

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
                      {racks.length} racks · {totalNodes} nodes · {onlineNodes} online
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
                  <RackView
                    key={rack.id}
                    rack={rack}
                    selectedNode={selectedNode}
                    onSelectNode={onSelectNode}
                  />
                ))}
              </div>
            </CardContent>
          </Card>
        )
      })}
    </div>
  )
}
