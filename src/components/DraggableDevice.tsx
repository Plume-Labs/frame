import { ClusterNode, DeviceType } from '@/lib/types'
import { DraggedDevice } from './DragDropRackManager'
import { 
  Desktop, 
  HardDrive, 
  WifiHigh, 
  Lightning, 
  BatteryCharging,
  Cpu,
  Memory,
  Database
} from '@phosphor-icons/react'

interface DraggableDeviceProps {
  node: ClusterNode
  isSelected: boolean
  onDragStart: (device: DraggedDevice) => void
  onDragEnd: () => void
  onSelect: (node: ClusterNode) => void
}

const deviceIcons: Record<DeviceType, React.ElementType> = {
  server: Desktop,
  storage: HardDrive,
  network: WifiHigh,
  pdu: Lightning,
  ups: BatteryCharging,
  blank: Cpu
}

const deviceColors: Record<DeviceType, string> = {
  server: 'bg-primary/20 border-primary text-primary',
  storage: 'bg-accent/20 border-accent text-accent',
  network: 'bg-blue-500/20 border-blue-500 text-blue-500',
  pdu: 'bg-yellow-500/20 border-yellow-500 text-yellow-500',
  ups: 'bg-green-500/20 border-green-500 text-green-500',
  blank: 'bg-muted border-border text-muted-foreground'
}

const statusColors = {
  online: 'bg-primary',
  degraded: 'bg-warning',
  offline: 'bg-destructive',
  provisioning: 'bg-blue-500'
}

export function DraggableDevice({ 
  node, 
  isSelected, 
  onDragStart, 
  onDragEnd, 
  onSelect 
}: DraggableDeviceProps) {
  const Icon = deviceIcons[node.hardware.deviceType]
  const height = node.hardware.rackUnits
  
  const handleDragStart = (e: React.DragEvent) => {
    e.dataTransfer.effectAllowed = 'move'
    onDragStart({ type: 'node', nodeId: node.id })
  }

  return (
    <div
      draggable
      onDragStart={handleDragStart}
      onDragEnd={onDragEnd}
      onClick={() => onSelect(node)}
      className={`
        relative border-2 cursor-move transition-all
        ${deviceColors[node.hardware.deviceType]}
        ${isSelected ? 'ring-2 ring-primary ring-offset-2' : ''}
        hover:brightness-110
      `}
      style={{ height: `${height * 20}px`, minHeight: `${height * 20}px` }}
    >
      <div className="h-full p-2 flex flex-col justify-between">
        <div className="flex items-start justify-between gap-1">
          <div className="flex items-center gap-1.5 min-w-0">
            <Icon weight="duotone" className="w-3.5 h-3.5 flex-shrink-0" />
            <span className="text-[10px] font-mono font-semibold truncate">
              {node.name}
            </span>
          </div>
          <div 
            className={`w-1.5 h-1.5 rounded-full flex-shrink-0 ${statusColors[node.status]}`}
            title={node.status}
          />
        </div>
        
        {height >= 2 && (
          <div className="flex flex-wrap gap-1 mt-1">
            {node.hardware.deviceType === 'server' && (
              <>
                <div className="flex items-center gap-0.5 text-[8px] font-mono">
                  <Cpu className="w-2.5 h-2.5" />
                  <span>{Math.round(node.metrics.cpu)}%</span>
                </div>
                <div className="flex items-center gap-0.5 text-[8px] font-mono">
                  <Memory className="w-2.5 h-2.5" />
                  <span>{Math.round(node.metrics.memory)}%</span>
                </div>
              </>
            )}
            {node.hardware.deviceType === 'storage' && (
              <div className="flex items-center gap-0.5 text-[8px] font-mono">
                <Database className="w-2.5 h-2.5" />
                <span>{Math.round(node.metrics.storage)}%</span>
              </div>
            )}
          </div>
        )}
        
        <div className="flex items-center justify-between mt-auto pt-1">
          <span className="text-[8px] font-mono opacity-70">
            U{node.rackPosition}
          </span>
          <span className="text-[8px] font-mono opacity-70">
            {node.hardware.rackUnits}U
          </span>
        </div>
      </div>
    </div>
  )
}
