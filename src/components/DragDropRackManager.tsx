import { useState, useMemo, useEffect } from 'react'
import { ClusterNode } from '@/lib/types'
import { organizeNodesByRack, organizeRacksByZone, RackData } from '@/lib/rack'
import { DraggableRack } from './DraggableRack'
import { DevicePalette } from './DevicePalette'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Buildings, ArrowCounterClockwise, CheckCircle, Database } from '@phosphor-icons/react'
import { toast } from 'sonner'
import { useKV } from '@github/spark/hooks'

interface DragDropRackManagerProps {
  nodes: ClusterNode[]
  onNodesUpdate: (nodes: ClusterNode[]) => void
  selectedNode: ClusterNode | null
  onSelectNode: (node: ClusterNode) => void
}

export interface DraggedDevice {
  type: 'node' | 'new-device'
  nodeId?: string
  deviceType?: string
  rackUnits?: number
}

export interface DropTarget {
  rackId: string
  position: number
}

export function DragDropRackManager({ 
  nodes, 
  onNodesUpdate,
  selectedNode, 
  onSelectNode 
}: DragDropRackManagerProps) {
  const [draggedDevice, setDraggedDevice] = useState<DraggedDevice | null>(null)
  const [dropTarget, setDropTarget] = useState<DropTarget | null>(null)
  const [pendingChanges, setPendingChanges] = useState<ClusterNode[]>(nodes)
  const [hasChanges, setHasChanges] = useState(false)
  const [savedLayouts, setSavedLayouts] = useKV<{ name: string; nodes: ClusterNode[]; timestamp: number }[]>('rack-layouts', [])

  useEffect(() => {
    setPendingChanges(nodes)
  }, [nodes])

  const { racksMap, zoneMap } = useMemo(() => {
    const racksMap = organizeNodesByRack(pendingChanges)
    const zoneMap = organizeRacksByZone(racksMap)
    return { racksMap, zoneMap }
  }, [pendingChanges])

  const handleDragStart = (device: DraggedDevice) => {
    setDraggedDevice(device)
  }

  const handleDragEnd = () => {
    setDraggedDevice(null)
    setDropTarget(null)
  }

  const handleDragOver = (target: DropTarget) => {
    setDropTarget(target)
  }

  const handleDrop = (target: DropTarget) => {
    if (!draggedDevice) return

    const updatedNodes = [...pendingChanges]

    if (draggedDevice.type === 'node' && draggedDevice.nodeId) {
      const nodeIndex = updatedNodes.findIndex(n => n.id === draggedDevice.nodeId)
      if (nodeIndex !== -1) {
        const existingNodeAtPosition = updatedNodes.find(
          n => n.rackId === target.rackId && n.rackPosition === target.position
        )

        if (existingNodeAtPosition && existingNodeAtPosition.id !== draggedDevice.nodeId) {
          const movedNode = updatedNodes[nodeIndex]
          updatedNodes[nodeIndex] = {
            ...movedNode,
            rackId: target.rackId,
            rackPosition: target.position
          }

          const existingIndex = updatedNodes.findIndex(n => n.id === existingNodeAtPosition.id)
          updatedNodes[existingIndex] = {
            ...existingNodeAtPosition,
            rackId: movedNode.rackId,
            rackPosition: movedNode.rackPosition
          }

          toast.success('Devices swapped', {
            description: `${movedNode.name} ↔ ${existingNodeAtPosition.name}`
          })
        } else {
          updatedNodes[nodeIndex] = {
            ...updatedNodes[nodeIndex],
            rackId: target.rackId,
            rackPosition: target.position
          }

          toast.success('Device moved', {
            description: `${updatedNodes[nodeIndex].name} → ${target.rackId} @ U${target.position}`
          })
        }

        setPendingChanges(updatedNodes)
        setHasChanges(true)
      }
    } else if (draggedDevice.type === 'new-device') {
      toast.info('New device planned', {
        description: `${draggedDevice.deviceType} will be provisioned at ${target.rackId} @ U${target.position}`
      })
    }

    setDraggedDevice(null)
    setDropTarget(null)
  }

  const handleApplyChanges = () => {
    onNodesUpdate(pendingChanges)
    setHasChanges(false)
    toast.success('Rack layout applied', {
      description: 'Device placements have been updated'
    })
  }

  const handleResetChanges = () => {
    setPendingChanges(nodes)
    setHasChanges(false)
    toast.info('Changes discarded', {
      description: 'Rack layout restored to original state'
    })
  }

  const handleSaveLayout = () => {
    const layoutName = `Layout ${new Date().toLocaleString()}`
    setSavedLayouts((current) => [
      ...(current || []),
      {
        name: layoutName,
        nodes: pendingChanges,
        timestamp: Date.now()
      }
    ].slice(-10))
    toast.success('Layout saved', {
      description: layoutName
    })
  }

  const handleLoadLayout = (layout: { name: string; nodes: ClusterNode[] }) => {
    setPendingChanges(layout.nodes)
    setHasChanges(true)
    toast.success('Layout loaded', {
      description: layout.name
    })
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col sm:flex-row justify-between items-start sm:items-center gap-4">
        <div>
          <h2 className="text-2xl font-mono font-bold text-foreground">Rack Organization</h2>
          <p className="text-sm text-muted-foreground font-mono mt-1">
            Drag and drop devices to reorganize your cluster layout
          </p>
        </div>
        <div className="flex gap-2 flex-wrap">
          {hasChanges && (
            <>
              <Button
                variant="outline"
                size="sm"
                onClick={handleResetChanges}
                className="font-mono"
              >
                <ArrowCounterClockwise className="w-4 h-4 mr-2" />
                Reset
              </Button>
              <Button
                variant="default"
                size="sm"
                onClick={handleApplyChanges}
                className="font-mono"
              >
                <CheckCircle className="w-4 h-4 mr-2" />
                Apply Changes
              </Button>
            </>
          )}
          <Button
            variant="secondary"
            size="sm"
            onClick={handleSaveLayout}
            className="font-mono"
          >
            <Database className="w-4 h-4 mr-2" />
            Save Layout
          </Button>
        </div>
      </div>

      {hasChanges && (
        <Card className="border-warning bg-warning/5">
          <CardContent className="pt-4">
            <p className="text-sm text-warning font-mono">
              You have unsaved changes. Click "Apply Changes" to update the cluster layout.
            </p>
          </CardContent>
        </Card>
      )}

      {savedLayouts && Array.isArray(savedLayouts) && savedLayouts.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm font-mono">Saved Layouts</CardTitle>
            <CardDescription className="font-mono">Load a previously saved rack configuration</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex flex-wrap gap-2">
              {savedLayouts.map((layout, idx) => (
                <Button
                  key={idx}
                  variant="outline"
                  size="sm"
                  onClick={() => handleLoadLayout(layout)}
                  className="font-mono"
                >
                  {layout.name}
                </Button>
              ))}
            </div>
          </CardContent>
        </Card>
      )}

      <DevicePalette onDragStart={handleDragStart} />

      <div className="space-y-6">
        {Array.from(zoneMap.entries()).map(([zoneName, racks]) => {
          const totalNodes = racks.reduce((sum, rack) => sum + rack.nodes.length, 0)
          const onlineNodes = racks.reduce((sum, rack) => 
            sum + rack.nodes.filter(n => n.status === 'online').length, 0
          )

          return (
            <Card key={zoneName} className="border-2">
              <CardHeader>
                <div className="flex items-center justify-between gap-4">
                  <div className="flex items-center gap-3">
                    <Buildings className="w-6 h-6 text-primary" weight="duotone" />
                    <div>
                      <CardTitle className="text-xl font-mono uppercase">{zoneName}</CardTitle>
                      <p className="text-sm text-muted-foreground font-mono mt-1">
                        {racks.length} racks · {totalNodes} nodes · {onlineNodes} online
                      </p>
                    </div>
                  </div>
                  <Badge variant="outline" className="bg-primary/20 text-primary border-primary font-mono">
                    {Math.round((onlineNodes / totalNodes) * 100)}% Online
                  </Badge>
                </div>
              </CardHeader>
              <CardContent>
                <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5 gap-6">
                  {racks.map((rack) => (
                    <DraggableRack
                      key={rack.id}
                      rack={rack}
                      draggedDevice={draggedDevice}
                      dropTarget={dropTarget}
                      onDragStart={handleDragStart}
                      onDragEnd={handleDragEnd}
                      onDragOver={handleDragOver}
                      onDrop={handleDrop}
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

      {draggedDevice && (
        <div className="fixed bottom-6 left-1/2 -translate-x-1/2 z-50">
          <Card className="border-2 border-primary shadow-lg">
            <CardContent className="pt-4 font-mono text-sm text-primary">
              {draggedDevice.type === 'node' 
                ? `Moving: ${pendingChanges.find(n => n.id === draggedDevice.nodeId)?.name}`
                : `Placing: ${draggedDevice.deviceType} (${draggedDevice.rackUnits}U)`
              }
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  )
}
