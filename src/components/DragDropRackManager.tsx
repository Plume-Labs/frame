import { useState, useMemo, useEffect } from 'react'
import { ClusterNode } from '@/lib/types'
import { organizeNodesByRack, organizeRacksByZone, RackData } from '@/lib/rack'
import { 
  validateRackPlacement, 
  validateDeviceMove, 
  ValidationResult,
  RackConstraints,
  DEFAULT_RACK_CONSTRAINTS,
  getValidationSummary
} from '@/lib/rackValidation'
import { DraggableRack } from './DraggableRack'
import { DevicePalette } from './DevicePalette'
import { RackValidationPanel } from './RackValidationPanel'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Buildings, ArrowCounterClockwise, CheckCircle, Database, Gear, ShieldWarning } from '@phosphor-icons/react'
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
  const [constraints, setConstraints] = useKV<RackConstraints>('rack-constraints', DEFAULT_RACK_CONSTRAINTS)
  const [showConstraintsDialog, setShowConstraintsDialog] = useState(false)
  const [showValidation, setShowValidation] = useState(true)
  const [selectedRackForValidation, setSelectedRackForValidation] = useState<string | null>(null)

  useEffect(() => {
    setPendingChanges(nodes)
  }, [nodes])

  const { racksMap, zoneMap } = useMemo(() => {
    const racksMap = organizeNodesByRack(pendingChanges)
    const zoneMap = organizeRacksByZone(racksMap)
    return { racksMap, zoneMap }
  }, [pendingChanges])

  const rackValidations = useMemo(() => {
    const validations = new Map<string, ValidationResult>()
    racksMap.forEach((rack, rackId) => {
      const validation = validateRackPlacement(rack, constraints || DEFAULT_RACK_CONSTRAINTS)
      validations.set(rackId, validation)
    })
    return validations
  }, [racksMap, constraints])

  const overallValid = useMemo(() => {
    return Array.from(rackValidations.values()).every(v => v.valid)
  }, [rackValidations])

  const totalErrors = useMemo(() => {
    return Array.from(rackValidations.values()).reduce((sum, v) => sum + v.errors.length, 0)
  }, [rackValidations])

  const totalWarnings = useMemo(() => {
    return Array.from(rackValidations.values()).reduce((sum, v) => sum + v.warnings.length, 0)
  }, [rackValidations])

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
        const movedNode = updatedNodes[nodeIndex]
        
        const sourceRackMap = organizeNodesByRack(updatedNodes)
        const sourceRack = sourceRackMap.get(movedNode.rackId)!
        const targetRack = sourceRackMap.get(target.rackId)!
        
        const validation = validateDeviceMove(
          sourceRack,
          targetRack,
          movedNode,
          target.position,
          constraints || DEFAULT_RACK_CONSTRAINTS
        )
        
        if (!validation.valid) {
          const criticalErrors = validation.errors.filter(e => e.severity === 'critical')
          if (criticalErrors.length > 0) {
            toast.error('Move blocked - Critical constraint violation', {
              description: criticalErrors[0].message
            })
            setDraggedDevice(null)
            setDropTarget(null)
            return
          }
        }
        
        const existingNodeAtPosition = updatedNodes.find(
          n => n.rackId === target.rackId && n.rackPosition === target.position
        )

        if (existingNodeAtPosition && existingNodeAtPosition.id !== draggedDevice.nodeId) {
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

          if (validation.warnings.length > 0) {
            toast.warning('Devices swapped with warnings', {
              description: `${validation.warnings.length} warning(s): ${validation.warnings[0].message}`
            })
          } else {
            toast.success('Devices swapped', {
              description: `${movedNode.name} ↔ ${existingNodeAtPosition.name}`
            })
          }
        } else {
          updatedNodes[nodeIndex] = {
            ...updatedNodes[nodeIndex],
            rackId: target.rackId,
            rackPosition: target.position
          }

          if (validation.warnings.length > 0) {
            toast.warning('Device moved with warnings', {
              description: `${validation.warnings.length} warning(s): ${validation.warnings[0].message}`
            })
          } else {
            toast.success('Device moved', {
              description: `${updatedNodes[nodeIndex].name} → ${target.rackId} @ U${target.position}`
            })
          }
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
          <Button
            variant="outline"
            size="sm"
            onClick={() => setShowConstraintsDialog(true)}
            className="font-mono"
          >
            <Gear className="w-4 h-4 mr-2" />
            Constraints
          </Button>
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
                disabled={!overallValid}
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

      {!overallValid && hasChanges && (
        <Card className="border-destructive bg-destructive/5">
          <CardContent className="pt-4 flex items-start gap-3">
            <ShieldWarning className="w-5 h-5 text-destructive flex-shrink-0 mt-0.5" weight="fill" />
            <div className="flex-1">
              <p className="text-sm font-mono font-semibold text-destructive">
                Critical Validation Errors Detected
              </p>
              <p className="text-xs text-muted-foreground font-mono mt-1">
                {totalErrors} error(s) across {Array.from(rackValidations.entries()).filter(([_, v]) => !v.valid).length} rack(s) must be resolved before applying changes
              </p>
            </div>
          </CardContent>
        </Card>
      )}

      {totalWarnings > 0 && overallValid && hasChanges && (
        <Card className="border-warning bg-warning/5">
          <CardContent className="pt-4 flex items-start gap-3">
            <ShieldWarning className="w-5 h-5 text-warning flex-shrink-0 mt-0.5" weight="fill" />
            <div className="flex-1">
              <p className="text-sm font-mono font-semibold text-warning">
                {totalWarnings} Warning(s) Detected
              </p>
              <p className="text-xs text-muted-foreground font-mono mt-1">
                Configuration meets minimum requirements but has optimization opportunities
              </p>
            </div>
          </CardContent>
        </Card>
      )}

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

      {showValidation && (
        <Tabs defaultValue="overview" className="w-full">
          <TabsList className="font-mono">
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="racks">By Rack</TabsTrigger>
          </TabsList>
          <TabsContent value="overview" className="space-y-4">
            <Card>
              <CardHeader>
                <CardTitle className="text-lg font-mono flex items-center gap-2">
                  <ShieldWarning className="w-5 h-5" weight="duotone" />
                  Validation Summary
                </CardTitle>
                <CardDescription className="font-mono">
                  Review constraint violations across all racks
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-4">
                {Array.from(rackValidations.entries())
                  .filter(([_, validation]) => !validation.valid || validation.warnings.length > 0)
                  .map(([rackId, validation]) => (
                    <div key={rackId} className="space-y-2">
                      <div className="flex items-center justify-between">
                        <h4 className="font-mono text-sm font-semibold">{rackId}</h4>
                        <Badge 
                          variant={validation.valid ? "outline" : "destructive"}
                          className="font-mono"
                        >
                          {getValidationSummary(validation)}
                        </Badge>
                      </div>
                      {(!validation.valid || validation.warnings.length > 0) && (
                        <Button
                          variant="link"
                          size="sm"
                          onClick={() => setSelectedRackForValidation(rackId)}
                          className="font-mono h-auto p-0 text-xs"
                        >
                          View Details →
                        </Button>
                      )}
                    </div>
                  ))}
                {Array.from(rackValidations.values()).every(v => v.valid && v.warnings.length === 0) && (
                  <p className="text-sm text-muted-foreground font-mono text-center py-4">
                    All racks meet validation requirements
                  </p>
                )}
              </CardContent>
            </Card>
          </TabsContent>
          <TabsContent value="racks" className="space-y-4">
            {Array.from(rackValidations.entries()).map(([rackId, validation]) => (
              <RackValidationPanel
                key={rackId}
                rackId={rackId}
                validation={validation}
              />
            ))}
          </TabsContent>
        </Tabs>
      )}

      {selectedRackForValidation && rackValidations.has(selectedRackForValidation) && (
        <Dialog open={!!selectedRackForValidation} onOpenChange={() => setSelectedRackForValidation(null)}>
          <DialogContent className="max-w-2xl">
            <RackValidationPanel
              rackId={selectedRackForValidation}
              validation={rackValidations.get(selectedRackForValidation)!}
            />
          </DialogContent>
        </Dialog>
      )}

      <Dialog open={showConstraintsDialog} onOpenChange={setShowConstraintsDialog}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle className="font-mono flex items-center gap-2">
              <Gear className="w-5 h-5" weight="duotone" />
              Rack Placement Constraints
            </DialogTitle>
            <DialogDescription className="font-mono">
              Configure physical, power, and cooling limits for rack validation
            </DialogDescription>
          </DialogHeader>
          <div className="grid grid-cols-2 gap-4 mt-4">
            <div className="space-y-2">
              <Label htmlFor="maxPowerWatts" className="font-mono text-sm">Max Power (W)</Label>
              <Input
                id="maxPowerWatts"
                type="number"
                value={constraints?.maxPowerWatts || DEFAULT_RACK_CONSTRAINTS.maxPowerWatts}
                onChange={(e) => setConstraints(c => ({ ...(c || DEFAULT_RACK_CONSTRAINTS), maxPowerWatts: parseInt(e.target.value) }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="maxCoolingBTU" className="font-mono text-sm">Max Cooling (BTU/h)</Label>
              <Input
                id="maxCoolingBTU"
                type="number"
                value={constraints?.maxCoolingBTU || DEFAULT_RACK_CONSTRAINTS.maxCoolingBTU}
                onChange={(e) => setConstraints(c => ({ ...(c || DEFAULT_RACK_CONSTRAINTS), maxCoolingBTU: parseInt(e.target.value) }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="maxUnits" className="font-mono text-sm">Max Rack Units</Label>
              <Input
                id="maxUnits"
                type="number"
                value={constraints?.maxUnits || DEFAULT_RACK_CONSTRAINTS.maxUnits}
                onChange={(e) => setConstraints(c => ({ ...(c || DEFAULT_RACK_CONSTRAINTS), maxUnits: parseInt(e.target.value) }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="minSpacingUnits" className="font-mono text-sm">Min Spacing (U)</Label>
              <Input
                id="minSpacingUnits"
                type="number"
                value={constraints?.minSpacingUnits || DEFAULT_RACK_CONSTRAINTS.minSpacingUnits}
                onChange={(e) => setConstraints(c => ({ ...(c || DEFAULT_RACK_CONSTRAINTS), minSpacingUnits: parseInt(e.target.value) }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="maxPowerDensity" className="font-mono text-sm">Max Power Density (W/U)</Label>
              <Input
                id="maxPowerDensity"
                type="number"
                value={constraints?.maxPowerDensity || DEFAULT_RACK_CONSTRAINTS.maxPowerDensity}
                onChange={(e) => setConstraints(c => ({ ...(c || DEFAULT_RACK_CONSTRAINTS), maxPowerDensity: parseInt(e.target.value) }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="maxAmperage" className="font-mono text-sm">Max Amperage (A)</Label>
              <Input
                id="maxAmperage"
                type="number"
                value={constraints?.maxAmperage || DEFAULT_RACK_CONSTRAINTS.maxAmperage}
                onChange={(e) => setConstraints(c => ({ ...(c || DEFAULT_RACK_CONSTRAINTS), maxAmperage: parseInt(e.target.value) }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="maxWeight" className="font-mono text-sm">Max Weight (kg)</Label>
              <Input
                id="maxWeight"
                type="number"
                value={constraints?.maxWeight || DEFAULT_RACK_CONSTRAINTS.maxWeight}
                onChange={(e) => setConstraints(c => ({ ...(c || DEFAULT_RACK_CONSTRAINTS), maxWeight: parseInt(e.target.value) }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="minAirflowCFM" className="font-mono text-sm">Min Airflow (CFM)</Label>
              <Input
                id="minAirflowCFM"
                type="number"
                value={constraints?.minAirflowCFM || DEFAULT_RACK_CONSTRAINTS.minAirflowCFM}
                onChange={(e) => setConstraints(c => ({ ...(c || DEFAULT_RACK_CONSTRAINTS), minAirflowCFM: parseInt(e.target.value) }))}
                className="font-mono"
              />
            </div>
          </div>
          <div className="flex justify-end gap-2 mt-4">
            <Button
              variant="outline"
              onClick={() => {
                setConstraints(DEFAULT_RACK_CONSTRAINTS)
                toast.info('Reset to defaults')
              }}
              className="font-mono"
            >
              Reset to Defaults
            </Button>
            <Button
              onClick={() => {
                setShowConstraintsDialog(false)
                toast.success('Constraints updated')
              }}
              className="font-mono"
            >
              Apply
            </Button>
          </div>
        </DialogContent>
      </Dialog>

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
