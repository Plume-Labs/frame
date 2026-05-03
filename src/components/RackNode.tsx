import { ClusterNode } from '@/lib/types'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Cpu, HardDrive, ChartBar, Thermometer } from '@phosphor-icons/react'
import { cn } from '@/lib/utils'

interface RackNodeProps {
  node: ClusterNode
  onSelect: () => void
  isSelected: boolean
}

export function RackNode({ node, onSelect, isSelected }: RackNodeProps) {
  const getStatusColor = () => {
    switch (node.status) {
      case 'online':
        return 'bg-primary/20 border-primary hover:bg-primary/30'
      case 'degraded':
        return 'bg-warning/20 border-warning hover:bg-warning/30'
      case 'offline':
        return 'bg-destructive/20 border-destructive hover:bg-destructive/30'
      case 'provisioning':
        return 'bg-accent/20 border-accent hover:bg-accent/30'
      default:
        return 'bg-muted border-border'
    }
  }

  const getMetricColor = (value: number) => {
    if (value > 85) return 'text-destructive'
    if (value > 70) return 'text-warning'
    return 'text-primary'
  }

  return (
    <div
      onClick={onSelect}
      className={cn(
        'relative h-16 border-2 rounded cursor-pointer transition-all duration-200',
        getStatusColor(),
        isSelected && 'ring-2 ring-ring ring-offset-2 ring-offset-background'
      )}
    >
      <div className="flex items-center justify-between h-full px-3 gap-2">
        <div className="flex flex-col justify-center min-w-0 flex-shrink">
          <span className="text-xs font-mono font-semibold truncate">
            {node.name}
          </span>
          <span className="text-[10px] font-mono text-muted-foreground truncate">
            U{node.rackPosition}
          </span>
        </div>

        <div className="flex gap-1.5 items-center flex-shrink-0">
          <div className="flex items-center gap-0.5">
            <Cpu className={cn('w-3 h-3', getMetricColor(node.metrics.cpu))} weight="fill" />
            <span className="text-[10px] font-mono font-semibold">
              {Math.round(node.metrics.cpu)}
            </span>
          </div>
          
          <div className="flex items-center gap-0.5">
            <ChartBar className={cn('w-3 h-3', getMetricColor(node.metrics.memory))} weight="fill" />
            <span className="text-[10px] font-mono font-semibold">
              {Math.round(node.metrics.memory)}
            </span>
          </div>
          
          <div className="flex items-center gap-0.5">
            <HardDrive className={cn('w-3 h-3', getMetricColor(node.metrics.storage))} weight="fill" />
            <span className="text-[10px] font-mono font-semibold">
              {Math.round(node.metrics.storage)}
            </span>
          </div>

          {node.hardware.temperature > 70 && (
            <Thermometer 
              className={cn(
                'w-3 h-3',
                node.hardware.temperature > 80 ? 'text-destructive' : 'text-warning'
              )} 
              weight="fill" 
            />
          )}
        </div>
      </div>
    </div>
  )
}
