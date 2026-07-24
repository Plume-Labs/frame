import { useState } from 'react'
import { ClusterNode } from '@/lib/types'
import { RackData } from '@/lib/rack'
import { DraggableDevice } from './DraggableDevice'
import { DropZone } from './DropZone'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { DraggedDevice, DropTarget } from './DragDropRackManager'

interface DraggableRackProps {
  rack: RackData
  draggedDevice: DraggedDevice | null
  dropTarget: DropTarget | null
  onDragStart: (device: DraggedDevice) => void
  onDragEnd: () => void
  onDragOver: (target: DropTarget) => void
  onDrop: (target: DropTarget) => void
  selectedNode: ClusterNode | null
  onSelectNode: (node: ClusterNode) => void
}

export function DraggableRack({
  rack,
  draggedDevice: _draggedDevice,
  dropTarget,
  onDragStart,
  onDragEnd,
  onDragOver,
  onDrop,
  selectedNode,
  onSelectNode
}: DraggableRackProps) {
  const [hoveredPosition, setHoveredPosition] = useState<number | null>(null)

  // A full 42U elevation renders ~840px tall while local racks hold only a
  // handful of devices, leaving the card mostly empty drop zones. Size the rack
  // to its tallest occupied slot plus a few free U for drop targets, with a
  // sensible minimum, so every rack stays compact and consistent.
  const maxOccupied = rack.nodes.reduce(
    (max, n) => Math.max(max, n.rackPosition + n.hardware.rackUnits - 1),
    0,
  )
  const RACK_HEIGHT = Math.max(maxOccupied + 4, 12)
  const positions = Array.from({ length: RACK_HEIGHT }, (_, i) => RACK_HEIGHT - i)
  
  const getNodeAtPosition = (position: number): ClusterNode | null => {
    return rack.nodes.find(n => n.rackPosition === position) || null
  }

  const isPositionOccupied = (position: number): boolean => {
    return rack.nodes.some(n => {
      const nodeStart = n.rackPosition
      const nodeEnd = n.rackPosition + n.hardware.rackUnits - 1
      return position >= nodeStart && position <= nodeEnd
    })
  }

  const handleDragOver = (e: React.DragEvent, position: number) => {
    e.preventDefault()
    setHoveredPosition(position)
    onDragOver({ rackId: rack.id, position })
  }

  const handleDragLeave = () => {
    setHoveredPosition(null)
  }

  const handleDrop = (e: React.DragEvent, position: number) => {
    e.preventDefault()
    setHoveredPosition(null)
    onDrop({ rackId: rack.id, position })
  }

  const isDropTargetPosition = (position: number) => {
    return dropTarget?.rackId === rack.id && dropTarget?.position === position
  }

  const getHealthColor = () => {
    if (rack.healthScore >= 80) return 'bg-primary/20 text-primary border-primary'
    if (rack.healthScore >= 60) return 'bg-warning/20 text-warning border-warning'
    return 'bg-destructive/20 text-destructive border-destructive'
  }

  return (
    <Card className="h-full">
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between gap-2">
          <CardTitle className="text-sm font-mono uppercase">{rack.id}</CardTitle>
          <Badge variant="outline" className={`${getHealthColor()} text-xs`}>
            {Math.round(rack.healthScore)}%
          </Badge>
        </div>
        <div className="text-xs text-muted-foreground font-mono mt-1">
          {rack.nodes.length} devices
        </div>
      </CardHeader>
      <CardContent className="space-y-0">
        <div className="relative border border-border rounded-md bg-card/50 overflow-hidden">
          {/* One 20px ruler row per U, matching the slot column exactly, so a
              device that occupies U4 lines up with the "4" label instead of the
              ruler drifting against the slots. */}
          <div className="absolute left-0 top-0 bottom-0 w-8 bg-muted/30 border-r border-border text-[8px] font-mono text-muted-foreground">
            {positions.map((pos, i) => (
              <div
                key={pos}
                className="flex items-center justify-center leading-none"
                style={{ height: '20px' }}
              >
                {i % 2 === 0 ? pos : ''}
              </div>
            ))}
          </div>

          <div className="ml-8 space-y-0">
            {positions.map((position) => {
              const node = getNodeAtPosition(position)
              const occupied = isPositionOccupied(position)
              const isHovered = hoveredPosition === position
              const isTarget = isDropTargetPosition(position)
              
              if (node && node.rackPosition === position) {
                return (
                  <DraggableDevice
                    key={`${rack.id}-${position}`}
                    node={node}
                    isSelected={selectedNode?.id === node.id}
                    onDragStart={onDragStart}
                    onDragEnd={onDragEnd}
                    onSelect={onSelectNode}
                  />
                )
              }
              
              if (!occupied) {
                return (
                  <DropZone
                    key={`${rack.id}-${position}`}
                    position={position}
                    rackId={rack.id}
                    isHovered={isHovered}
                    isTarget={isTarget}
                    onDragOver={handleDragOver}
                    onDragLeave={handleDragLeave}
                    onDrop={handleDrop}
                  />
                )
              }
              
              return null
            })}
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
