import { ClusterNode, RackPowerCooling } from '@/lib/types'
import { RackData } from '@/lib/rack'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { RackNode } from './RackNode'
import { HardDrives, CheckCircle, WarningCircle, Lightning, ThermometerSimple, Ruler } from '@phosphor-icons/react'
import { Progress } from '@/components/ui/progress'
import { useState } from 'react'

interface RackViewProps {
  rack: RackData
  powerCooling?: RackPowerCooling
  selectedNode: ClusterNode | null
  onSelectNode: (node: ClusterNode) => void
  showUnits?: boolean
}

export function RackView({ rack, powerCooling, selectedNode, onSelectNode, showUnits = true }: RackViewProps) {
  const [showUnitRuler, setShowUnitRuler] = useState(showUnits)
  const getHealthBadgeColor = () => {
    if (rack.healthScore >= 80) return 'bg-primary/20 text-primary border-primary'
    if (rack.healthScore >= 60) return 'bg-warning/20 text-warning border-warning'
    return 'bg-destructive/20 text-destructive border-destructive'
  }

  const storagePercent = rack.totalCapacity > 0 
    ? (rack.usedCapacity / rack.totalCapacity) * 100 
    : 0

  const powerUtilization = powerCooling 
    ? (powerCooling.power.currentDraw / powerCooling.power.maxCapacity) * 100 
    : 0

  const getTempColor = (temp: number) => {
    if (temp > 35) return 'text-destructive'
    if (temp > 32) return 'text-warning'
    return 'text-primary'
  }

  const totalRackUnits = 42
  const usedUnits = rack.nodes.reduce((sum, node) => sum + node.hardware.rackUnits, 0)
  const unitUtilization = (usedUnits / totalRackUnits) * 100

  return (
    <Card className="bg-card/50 border-2">
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <HardDrives className="w-5 h-5 text-primary" weight="duotone" />
            <CardTitle className="text-base font-mono">{rack.id}</CardTitle>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => setShowUnitRuler(!showUnitRuler)}
              className="p-1 hover:bg-accent rounded transition-colors"
              title={showUnitRuler ? 'Hide unit ruler' : 'Show unit ruler'}
            >
              <Ruler className="w-4 h-4 text-muted-foreground" weight="duotone" />
            </button>
            <Badge variant="outline" className={getHealthBadgeColor()}>
              {rack.healthScore >= 80 ? (
                <CheckCircle className="w-3 h-3 mr-1" weight="fill" />
              ) : (
                <WarningCircle className="w-3 h-3 mr-1" weight="fill" />
              )}
              {Math.round(rack.healthScore)}%
            </Badge>
          </div>
        </div>
        
        <div className="space-y-1.5 pt-2">
          <div className="flex justify-between text-xs">
            <span className="text-muted-foreground font-mono">Nodes</span>
            <span className="font-mono font-semibold">{rack.nodes.length}</span>
          </div>
          <div className="flex justify-between text-xs">
            <span className="text-muted-foreground font-mono">Units</span>
            <span className="font-mono font-semibold">{usedUnits} / {totalRackUnits}U</span>
          </div>
          <Progress value={unitUtilization} className="h-1" />
          
          <div className="space-y-1 pt-1">
            <div className="flex justify-between text-xs">
              <span className="text-muted-foreground font-mono">Storage</span>
              <span className="font-mono font-semibold">
                {Math.round(storagePercent)}%
              </span>
            </div>
            <Progress value={storagePercent} className="h-1" />
          </div>

          {powerCooling && (
            <>
              <div className="space-y-1 pt-1">
                <div className="flex justify-between text-xs">
                  <span className="text-muted-foreground font-mono flex items-center gap-1">
                    <Lightning className="w-3 h-3" weight="duotone" />
                    Power
                  </span>
                  <span className="font-mono font-semibold">
                    {powerCooling.power.currentDraw}W
                  </span>
                </div>
                <Progress value={powerUtilization} className="h-1" />
              </div>

              <div className="flex justify-between text-xs pt-1">
                <span className="text-muted-foreground font-mono flex items-center gap-1">
                  <ThermometerSimple className="w-3 h-3" weight="duotone" />
                  Outlet
                </span>
                <span className={`font-mono font-semibold ${getTempColor(powerCooling.cooling.outletTemp)}`}>
                  {powerCooling.cooling.outletTemp}°C
                </span>
              </div>
            </>
          )}
        </div>
      </CardHeader>

      <CardContent className="space-y-2 pt-0">
        <div className="flex gap-2">
          {showUnitRuler && (
            <div className="flex flex-col justify-between py-1 pr-2 border-r border-border">
              {Array.from({ length: Math.min(10, totalRackUnits) }).map((_, i) => {
                const unit = i * Math.floor(totalRackUnits / 10)
                return (
                  <div key={unit} className="text-[8px] font-mono text-muted-foreground text-right">
                    {unit}
                  </div>
                )
              })}
            </div>
          )}
          <div className="flex-1 space-y-2">
            {rack.nodes.map((node) => (
              <RackNode
                key={node.id}
                node={node}
                onSelect={() => onSelectNode(node)}
                isSelected={selectedNode?.id === node.id}
              />
            ))}
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
