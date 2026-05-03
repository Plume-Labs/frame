import { useState } from 'react'
import { DeviceType } from '@/lib/types'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Gear, Cpu, Database, WifiHigh } from '@phosphor-icons/react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

export interface DeviceConfig {
  name: string
  deviceType: DeviceType
  rackUnits: number
  cpuCores: number
  memoryGB: number
  storageGB: number
  networkAdapters: number
  rackId: string
  rackPosition: number
}

interface RackConfigDialogProps {
  onAddDevice?: (config: DeviceConfig) => void
}

export function RackConfigDialog({ onAddDevice }: RackConfigDialogProps) {
  const [open, setOpen] = useState(false)
  const [config, setConfig] = useState<DeviceConfig>({
    name: '',
    deviceType: 'server',
    rackUnits: 1,
    cpuCores: 48,
    memoryGB: 256,
    storageGB: 2048,
    networkAdapters: 2,
    rackId: '',
    rackPosition: 1
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    onAddDevice?.(config)
    setOpen(false)
    setConfig({
      name: '',
      deviceType: 'server',
      rackUnits: 1,
      cpuCores: 48,
      memoryGB: 256,
      storageGB: 2048,
      networkAdapters: 2,
      rackId: '',
      rackPosition: 1
    })
  }

  const getDeviceIcon = (type: DeviceType) => {
    switch (type) {
      case 'storage':
        return <Database className="w-5 h-5" weight="duotone" />
      case 'network':
        return <WifiHigh className="w-5 h-5" weight="duotone" />
      default:
        return <Cpu className="w-5 h-5" weight="duotone" />
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" className="gap-2">
          <Gear weight="duotone" />
          Configure Device
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="font-mono">Device Provisioning Configuration</DialogTitle>
          <DialogDescription>
            Configure device specifications and rack placement for PXE provisioning
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-6">
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="device-name" className="font-mono">Device Name</Label>
              <Input
                id="device-name"
                value={config.name}
                onChange={(e) => setConfig({ ...config, name: e.target.value })}
                placeholder="node-01"
                required
                className="font-mono"
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="device-type" className="font-mono">Device Type</Label>
              <Select
                value={config.deviceType}
                onValueChange={(value) => setConfig({ ...config, deviceType: value as DeviceType })}
              >
                <SelectTrigger id="device-type" className="font-mono">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="server" className="font-mono">
                    <div className="flex items-center gap-2">
                      <Cpu className="w-4 h-4" weight="duotone" />
                      Server
                    </div>
                  </SelectItem>
                  <SelectItem value="storage" className="font-mono">
                    <div className="flex items-center gap-2">
                      <Database className="w-4 h-4" weight="duotone" />
                      Storage
                    </div>
                  </SelectItem>
                  <SelectItem value="network" className="font-mono">
                    <div className="flex items-center gap-2">
                      <WifiHigh className="w-4 h-4" weight="duotone" />
                      Network
                    </div>
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <Card className="bg-muted/30">
            <CardHeader>
              <CardTitle className="text-sm font-mono">Rack Placement</CardTitle>
              <CardDescription className="text-xs">Specify physical rack location and unit size</CardDescription>
            </CardHeader>
            <CardContent className="grid grid-cols-3 gap-4">
              <div className="space-y-2">
                <Label htmlFor="rack-id" className="font-mono text-xs">Rack ID</Label>
                <Input
                  id="rack-id"
                  value={config.rackId}
                  onChange={(e) => setConfig({ ...config, rackId: e.target.value })}
                  placeholder="R01"
                  required
                  className="font-mono"
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="rack-position" className="font-mono text-xs">Position (U)</Label>
                <Input
                  id="rack-position"
                  type="number"
                  min="1"
                  max="42"
                  value={config.rackPosition}
                  onChange={(e) => setConfig({ ...config, rackPosition: parseInt(e.target.value) })}
                  required
                  className="font-mono"
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="rack-units" className="font-mono text-xs">Size (U)</Label>
                <Select
                  value={config.rackUnits.toString()}
                  onValueChange={(value) => setConfig({ ...config, rackUnits: parseInt(value) })}
                >
                  <SelectTrigger id="rack-units" className="font-mono">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="1" className="font-mono">1U</SelectItem>
                    <SelectItem value="2" className="font-mono">2U</SelectItem>
                    <SelectItem value="4" className="font-mono">4U</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>

          <Card className="bg-muted/30">
            <CardHeader>
              <CardTitle className="text-sm font-mono">Hardware Specifications</CardTitle>
              <CardDescription className="text-xs">Define device resources and capabilities</CardDescription>
            </CardHeader>
            <CardContent className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="cpu-cores" className="font-mono text-xs">CPU Cores</Label>
                <Input
                  id="cpu-cores"
                  type="number"
                  min="1"
                  value={config.cpuCores}
                  onChange={(e) => setConfig({ ...config, cpuCores: parseInt(e.target.value) })}
                  required
                  className="font-mono"
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="memory" className="font-mono text-xs">Memory (GB)</Label>
                <Input
                  id="memory"
                  type="number"
                  min="1"
                  value={config.memoryGB}
                  onChange={(e) => setConfig({ ...config, memoryGB: parseInt(e.target.value) })}
                  required
                  className="font-mono"
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="storage" className="font-mono text-xs">Storage (GB)</Label>
                <Input
                  id="storage"
                  type="number"
                  min="1"
                  value={config.storageGB}
                  onChange={(e) => setConfig({ ...config, storageGB: parseInt(e.target.value) })}
                  required
                  className="font-mono"
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="network-adapters" className="font-mono text-xs">Network Adapters</Label>
                <Input
                  id="network-adapters"
                  type="number"
                  min="1"
                  max="8"
                  value={config.networkAdapters}
                  onChange={(e) => setConfig({ ...config, networkAdapters: parseInt(e.target.value) })}
                  required
                  className="font-mono"
                />
              </div>
            </CardContent>
          </Card>

          <div className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit">
              Add Device
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
