import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import { DraggedDevice } from './DragDropRackManager'
import { 
  Desktop, 
  HardDrive, 
  WifiHigh, 
  Lightning, 
  BatteryCharging 
} from '@phosphor-icons/react'

interface DeviceTemplate {
  type: string
  displayName: string
  rackUnits: number
  icon: React.ElementType
  color: string
}

const deviceTemplates: DeviceTemplate[] = [
  {
    type: 'server-1u',
    displayName: '1U Server',
    rackUnits: 1,
    icon: Desktop,
    color: 'bg-primary/20 border-primary text-primary'
  },
  {
    type: 'server-2u',
    displayName: '2U Server',
    rackUnits: 2,
    icon: Desktop,
    color: 'bg-primary/20 border-primary text-primary'
  },
  {
    type: 'storage-4u',
    displayName: '4U Storage',
    rackUnits: 4,
    icon: HardDrive,
    color: 'bg-accent/20 border-accent text-accent'
  },
  {
    type: 'network-1u',
    displayName: '1U Network Switch',
    rackUnits: 1,
    icon: WifiHigh,
    color: 'bg-blue-500/20 border-blue-500 text-blue-500'
  },
  {
    type: 'pdu-0u',
    displayName: 'PDU',
    rackUnits: 1,
    icon: Lightning,
    color: 'bg-yellow-500/20 border-yellow-500 text-yellow-500'
  },
  {
    type: 'ups-2u',
    displayName: '2U UPS',
    rackUnits: 2,
    icon: BatteryCharging,
    color: 'bg-green-500/20 border-green-500 text-green-500'
  }
]

interface DevicePaletteProps {
  onDragStart: (device: DraggedDevice) => void
}

export function DevicePalette({ onDragStart }: DevicePaletteProps) {
  const handleDragStart = (template: DeviceTemplate) => {
    onDragStart({
      type: 'new-device',
      deviceType: template.displayName,
      rackUnits: template.rackUnits
    })
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm font-mono">Device Palette</CardTitle>
        <CardDescription className="font-mono">
          Drag devices to add them to racks
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-6 gap-3">
          {deviceTemplates.map((template) => {
            const Icon = template.icon
            return (
              <div
                key={template.type}
                draggable
                onDragStart={() => handleDragStart(template)}
                className={`
                  flex flex-col items-center justify-center gap-2 p-3
                  border-2 rounded-md cursor-grab active:cursor-grabbing
                  transition-all hover:scale-105 hover:shadow-md
                  ${template.color}
                `}
              >
                <Icon weight="duotone" className="w-6 h-6" />
                <div className="text-center">
                  <div className="text-xs font-mono font-semibold">
                    {template.displayName}
                  </div>
                  <div className="text-[10px] font-mono opacity-70">
                    {template.rackUnits}U
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      </CardContent>
    </Card>
  )
}
