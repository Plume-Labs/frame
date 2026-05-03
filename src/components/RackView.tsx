import { ClusterNode } from '@/lib/types'
import { RackData } from '@/lib/rack'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { RackNode } from './RackNode'
import { HardDrives, CheckCircle, WarningCircle } from '@phosphor-icons/react'
import { Progress } from '@/components/ui/progress'

interface RackViewProps {
  rack: RackData
  selectedNode: ClusterNode | null
  onSelectNode: (node: ClusterNode) => void
}

export function RackView({ rack, selectedNode, onSelectNode }: RackViewProps) {
  const getHealthBadgeColor = () => {
    if (rack.healthScore >= 80) return 'bg-primary/20 text-primary border-primary'
    if (rack.healthScore >= 60) return 'bg-warning/20 text-warning border-warning'
    return 'bg-destructive/20 text-destructive border-destructive'
  }

  const storagePercent = rack.totalCapacity > 0 
    ? (rack.usedCapacity / rack.totalCapacity) * 100 
    : 0

  return (
    <Card className="bg-card/50 border-2">
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <HardDrives className="w-5 h-5 text-primary" weight="duotone" />
            <CardTitle className="text-base font-mono">{rack.id}</CardTitle>
          </div>
          <Badge variant="outline" className={getHealthBadgeColor()}>
            {rack.healthScore >= 80 ? (
              <CheckCircle className="w-3 h-3 mr-1" weight="fill" />
            ) : (
              <WarningCircle className="w-3 h-3 mr-1" weight="fill" />
            )}
            {Math.round(rack.healthScore)}%
          </Badge>
        </div>
        
        <div className="space-y-1.5 pt-2">
          <div className="flex justify-between text-xs">
            <span className="text-muted-foreground font-mono">Nodes</span>
            <span className="font-mono font-semibold">{rack.nodes.length}</span>
          </div>
          <div className="space-y-1">
            <div className="flex justify-between text-xs">
              <span className="text-muted-foreground font-mono">Storage</span>
              <span className="font-mono font-semibold">
                {Math.round(storagePercent)}%
              </span>
            </div>
            <Progress value={storagePercent} className="h-1" />
          </div>
        </div>
      </CardHeader>

      <CardContent className="space-y-2 pt-0">
        {rack.nodes.map((node) => (
          <RackNode
            key={node.id}
            node={node}
            onSelect={() => onSelectNode(node)}
            isSelected={selectedNode?.id === node.id}
          />
        ))}
      </CardContent>
    </Card>
  )
}
