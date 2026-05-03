import { ClusterNode } from '@/lib/types'
import { Card } from '@/components/ui/card'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { HardDrives, Warning, XCircle, ArrowsClockwise } from '@phosphor-icons/react'
import { cn } from '@/lib/utils'

interface NodeCardProps {
  node: ClusterNode
  onClick: () => void
  isSelected: boolean
}

export function NodeCard({ node, onClick, isSelected }: NodeCardProps) {
  const statusColors = {
    online: 'text-accent border-accent/50',
    degraded: 'text-[oklch(0.75_0.18_75)] border-[oklch(0.75_0.18_75)]/50',
    offline: 'text-destructive border-destructive/50',
    provisioning: 'text-primary border-primary/50'
  }

  const statusIcons = {
    online: <HardDrives className="status-pulse" />,
    degraded: <Warning />,
    offline: <XCircle />,
    provisioning: <ArrowsClockwise className="animate-spin" />
  }

  const statusColor = statusColors[node.status]
  const StatusIcon = statusIcons[node.status]

  return (
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Card
            className={cn(
              'relative p-3 cursor-pointer transition-all duration-200 hover:scale-105',
              'border-2',
              statusColor,
              isSelected && 'ring-2 ring-primary ring-offset-2 ring-offset-background'
            )}
            onClick={onClick}
          >
            <div className="flex flex-col items-center gap-2">
              <div className={cn('text-2xl', statusColor)}>
                {StatusIcon}
              </div>
              <div className="text-xs font-mono text-muted-foreground text-center">
                {node.name}
              </div>
            </div>
          </Card>
        </TooltipTrigger>
        <TooltipContent side="top" className="font-mono text-xs">
          <div className="space-y-1">
            <div className="font-semibold">{node.id}</div>
            <div className="text-muted-foreground">
              CPU: {node.metrics.cpu.toFixed(1)}% | MEM: {node.metrics.memory.toFixed(1)}%
            </div>
          </div>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}
