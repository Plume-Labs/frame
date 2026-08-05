import { ClusterNode } from '@/lib/types'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Progress } from '@/components/ui/progress'
import { Separator } from '@/components/ui/separator'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { 
  Cpu, 
  HardDrives, 
  Network, 
  Database, 
  Thermometer,
  ArrowUp,
  ArrowDown,
  Clock,
  CheckCircle
} from '@phosphor-icons/react'
import { formatUptime, formatBytes, formatBandwidth } from '@/lib/cluster'
import { TONE_TEXT, inverseScoreTone, scoreTone } from '@/lib/thresholds'
import { useIsMobile } from '@/hooks/use-mobile'

interface NodeDetailPanelProps {
  node: ClusterNode | null
  open: boolean
  onClose: () => void
}

function formatNumber(num: number): string {
  return num.toLocaleString()
}

function formatBytesData(bytes: number): string {
  if (bytes >= 1e12) return `${(bytes / 1e12).toFixed(2)} TB`
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(2)} GB`
  if (bytes >= 1e6) return `${(bytes / 1e6).toFixed(2)} MB`
  return `${(bytes / 1e3).toFixed(2)} KB`
}

export function NodeDetailPanel({ node, open, onClose }: NodeDetailPanelProps) {
  const isMobile = useIsMobile()

  if (!node) return null

  const statusVariants = {
    online: 'bg-accent text-accent-foreground',
    degraded: 'bg-warning text-[oklch(0.15_0.02_240)]',
    offline: 'bg-destructive text-destructive-foreground',
    provisioning: 'bg-primary text-primary-foreground'
  }

  const metricItems = [
    { icon: Cpu, label: 'CPU Usage', value: node.metrics.cpu, unit: '%' },
    { icon: HardDrives, label: 'Memory', value: node.metrics.memory, unit: '%' },
    { icon: Database, label: 'Storage', value: node.metrics.storage, unit: '%' }
  ]

  return (
    <Sheet open={open} onOpenChange={(isOpen) => !isOpen && onClose()}>
      <SheetContent side={isMobile ? 'bottom' : 'right'} className="w-full sm:max-w-2xl overflow-y-auto">
        <SheetHeader>
          <SheetTitle className="font-mono text-2xl flex items-center justify-between">
            {node.name}
            <Badge className={statusVariants[node.status]}>
              {node.status}
            </Badge>
          </SheetTitle>
        </SheetHeader>
        
        <div className="mt-6 space-y-6">
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase">Node ID</div>
              <div className="font-mono text-sm">{node.id}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase">Rack</div>
              <div className="font-mono text-sm">{node.rackId}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase">Uptime</div>
              <div className="font-mono text-sm">{formatUptime(node.uptime)}</div>
            </div>
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground uppercase">Last Seen</div>
              <div className="font-mono text-xs">
                {new Date(node.lastSeen).toLocaleTimeString()}
              </div>
            </div>
          </div>

          <Separator />

          <Tabs defaultValue="metrics" className="w-full">
            <TabsList className="grid w-full grid-cols-5">
              <TabsTrigger value="metrics">Metrics</TabsTrigger>
              <TabsTrigger value="hardware">Hardware</TabsTrigger>
              <TabsTrigger value="network">Network</TabsTrigger>
              <TabsTrigger value="storage">Storage</TabsTrigger>
              <TabsTrigger value="resources">Resources</TabsTrigger>
            </TabsList>
            
            <TabsContent value="metrics" className="space-y-4 mt-4">
              {metricItems.map(({ icon: Icon, label, value, unit }) => (
                <div key={label} className="space-y-2">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <Icon className="text-primary" />
                      <span className="text-sm font-medium">{label}</span>
                    </div>
                    <span className="font-mono text-sm font-semibold">
                      {value.toFixed(1)}{unit}
                    </span>
                  </div>
                  <Progress value={value} className="h-2" />
                </div>
              ))}
            </TabsContent>

            <TabsContent value="hardware" className="space-y-4 mt-4">
              <div className="space-y-3">
                <div className="flex items-center justify-between p-3 rounded-lg bg-secondary/30">
                  <div className="flex items-center gap-2">
                    <Cpu className="text-primary" />
                    <span className="text-sm font-medium">CPU</span>
                  </div>
                  <div className="text-right">
                    <div className="font-mono text-sm font-semibold">{node.hardware.cpuModel}</div>
                    <div className="text-xs text-muted-foreground">{node.hardware.cpuCores} cores</div>
                  </div>
                </div>

                <div className="flex items-center justify-between p-3 rounded-lg bg-secondary/30">
                  <div className="flex items-center gap-2">
                    <HardDrives className="text-primary" />
                    <span className="text-sm font-medium">Memory</span>
                  </div>
                  <div className="font-mono text-sm font-semibold">{node.hardware.memoryGB} GB</div>
                </div>

                <div className="flex items-center justify-between p-3 rounded-lg bg-secondary/30">
                  <div className="flex items-center gap-2">
                    <Database className="text-primary" />
                    <span className="text-sm font-medium">Storage</span>
                  </div>
                  <div className="font-mono text-sm font-semibold">{formatBytes(node.hardware.storageGB)}</div>
                </div>

                <div className="flex items-center justify-between p-3 rounded-lg bg-secondary/30">
                  <div className="flex items-center gap-2">
                    <Network className="text-primary" />
                    <span className="text-sm font-medium">Network Adapters</span>
                  </div>
                  <div className="font-mono text-sm font-semibold">{node.hardware.networkAdapters}</div>
                </div>

                <div className="flex items-center justify-between p-3 rounded-lg bg-secondary/30">
                  <div className="flex items-center gap-2">
                    <Thermometer className="text-primary" />
                    <span className="text-sm font-medium">Temperature</span>
                  </div>
                  <div className={`font-mono text-sm font-semibold ${TONE_TEXT[inverseScoreTone(node.hardware.temperature, 75, 85)]}`}>
                    {node.hardware.temperature.toFixed(1)}°C
                  </div>
                </div>

                <div className="flex items-center justify-between p-3 rounded-lg bg-secondary/30">
                  <div className="flex items-center gap-2">
                    <CheckCircle className="text-primary" />
                    <span className="text-sm font-medium">PXE Booted</span>
                  </div>
                  <Badge variant={node.hardware.pxeBooted ? "default" : "secondary"}>
                    {node.hardware.pxeBooted ? 'Yes' : 'No'}
                  </Badge>
                </div>
              </div>
            </TabsContent>

            <TabsContent value="network" className="space-y-4 mt-4">
              <div className="space-y-3">
                <div className="p-3 rounded-lg bg-secondary/30">
                  <div className="flex items-center justify-between mb-2">
                    <span className="text-xs text-muted-foreground uppercase">RDMA Status</span>
                    <Badge variant={node.network.rdmaActive ? "default" : "secondary"}>
                      {node.network.rdmaActive ? 'Active' : 'Inactive'}
                    </Badge>
                  </div>
                  {node.network.rdmaActive && (
                    <div className="text-sm">
                      <span className="text-muted-foreground">Queue Pairs: </span>
                      <span className="font-mono font-semibold">{node.network.rdmaQueuePairs}</span>
                    </div>
                  )}
                </div>

                <div className="grid grid-cols-2 gap-3">
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="flex items-center gap-2 mb-2">
                      <ArrowDown className="text-accent text-sm" />
                      <span className="text-xs text-muted-foreground uppercase">Received</span>
                    </div>
                    <div className="font-mono text-lg font-bold">
                      {formatBytesData(node.network.rxBytes)}
                    </div>
                  </div>

                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="flex items-center gap-2 mb-2">
                      <ArrowUp className="text-primary text-sm" />
                      <span className="text-xs text-muted-foreground uppercase">Transmitted</span>
                    </div>
                    <div className="font-mono text-lg font-bold">
                      {formatBytesData(node.network.txBytes)}
                    </div>
                  </div>
                </div>

                <div className="grid grid-cols-3 gap-3">
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Bandwidth</div>
                    <div className="font-mono text-sm font-bold">
                      {formatBandwidth(node.network.bandwidth)}
                    </div>
                  </div>

                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="flex items-center gap-1 mb-1">
                      <Clock className="text-xs" />
                      <div className="text-xs text-muted-foreground uppercase">Latency</div>
                    </div>
                    <div className={`font-mono text-sm font-bold ${TONE_TEXT[inverseScoreTone(node.network.latency, 3, 10)]}`}>
                      {node.network.latency.toFixed(2)} ms
                    </div>
                  </div>

                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Packet Loss</div>
                    <div className={`font-mono text-sm font-bold ${TONE_TEXT[inverseScoreTone(node.network.packetLoss, 0.3, 1)]}`}>
                      {node.network.packetLoss.toFixed(3)}%
                    </div>
                  </div>
                </div>
              </div>
            </TabsContent>

            <TabsContent value="storage" className="space-y-4 mt-4">
              <div className="space-y-3">
                <div className="p-3 rounded-lg bg-secondary/30">
                  <div className="text-xs text-muted-foreground uppercase mb-2">Ceph Configuration</div>
                  <div className="grid grid-cols-3 gap-3 text-sm">
                    <div>
                      <div className="text-muted-foreground text-xs">OSDs</div>
                      <div className="font-mono font-semibold">{node.storage.cephOSDs}</div>
                    </div>
                    <div>
                      <div className="text-muted-foreground text-xs">PGs</div>
                      <div className="font-mono font-semibold">{node.storage.cephPGs}</div>
                    </div>
                    <div>
                      <div className="text-muted-foreground text-xs">Replication</div>
                      <div className="font-mono font-semibold">{node.storage.replicationFactor}x</div>
                    </div>
                  </div>
                </div>

                <div className="p-3 rounded-lg bg-secondary/30">
                  <div className="flex items-center justify-between mb-2">
                    <span className="text-xs text-muted-foreground uppercase">Capacity</span>
                    <span className="font-mono text-xs">
                      {formatBytes(node.storage.usedCapacity)} / {formatBytes(node.storage.totalCapacity)}
                    </span>
                  </div>
                  <Progress 
                    value={(node.storage.usedCapacity / node.storage.totalCapacity) * 100} 
                    className="h-2 mb-1" 
                  />
                  <div className="text-xs text-muted-foreground text-right">
                    {((node.storage.usedCapacity / node.storage.totalCapacity) * 100).toFixed(1)}% used
                  </div>
                </div>

                <div className="grid grid-cols-2 gap-3">
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Read IOPS</div>
                    <div className="font-mono text-lg font-bold">
                      {formatNumber(node.storage.readIOPS)}
                    </div>
                  </div>

                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Write IOPS</div>
                    <div className="font-mono text-lg font-bold">
                      {formatNumber(node.storage.writeIOPS)}
                    </div>
                  </div>
                </div>
              </div>
            </TabsContent>

            {/* Resource Isolation Panel — GPU MIG, hugepages, CPU pinning */}
            <TabsContent value="resources" className="space-y-4 mt-4">
              <div className="space-y-3">
                <div className="p-3 rounded-lg bg-secondary/30">
                  <div className="text-xs text-muted-foreground uppercase mb-2">GPU MIG Instances</div>
                  <div className="flex items-center justify-between">
                    <span className="font-mono text-2xl font-bold text-primary">{node.hardware.gpuMIGInstances}</span>
                    <span className="text-xs text-muted-foreground">MIG partitions active</span>
                  </div>
                  {node.gpuMetrics && node.gpuMetrics.length > 0 && (
                    <div className="mt-2 space-y-1">
                      {node.gpuMetrics.map(gpu => (
                        <div key={gpu.gpuIndex} className="flex items-center justify-between text-xs">
                          <span className="font-mono text-muted-foreground">GPU {gpu.gpuIndex} — {gpu.model}</span>
                          <span className={`font-mono font-bold ${gpu.migEnabled ? 'text-accent' : 'text-muted-foreground'}`}>
                            {gpu.migEnabled ? `MIG ×${gpu.migInstances}` : 'Full GPU'}
                          </span>
                        </div>
                      ))}
                    </div>
                  )}
                </div>

                <div className="grid grid-cols-2 gap-3">
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Hugepages</div>
                    <div className="font-mono text-lg font-bold text-primary">{node.hardware.hugepagesGB} GB</div>
                    <div className="text-xs text-muted-foreground">1 GiB pages</div>
                  </div>
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Pinned CPU Cores</div>
                    <div className="font-mono text-lg font-bold">{node.hardware.cpuPinnedCores}</div>
                    <div className="text-xs text-muted-foreground">of {node.hardware.cpuCores} total</div>
                  </div>
                </div>

                <div className="grid grid-cols-2 gap-3">
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">NUMA Node</div>
                    <div className="font-mono text-lg font-bold text-accent">{node.hardware.numaNode}</div>
                  </div>
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Topology Manager</div>
                    <div className="font-mono text-sm font-bold">{node.hardware.topologyManagerPolicy}</div>
                  </div>
                </div>

                <div className="grid grid-cols-2 gap-3">
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Cache Hit Rate</div>
                    <div className={`font-mono text-lg font-bold ${TONE_TEXT[scoreTone(node.hardware.cacheHitRate, 0.7, 0.4)]}`}>
                      {(node.hardware.cacheHitRate * 100).toFixed(1)}%
                    </div>
                    <div className="text-xs text-muted-foreground">Alluxio / Redis</div>
                  </div>
                  <div className="p-3 rounded-lg bg-secondary/30">
                    <div className="text-xs text-muted-foreground uppercase mb-1">Storage Tier</div>
                    <div className={`font-mono text-lg font-bold ${
                      node.hardware.storageTier === 'ram' ? 'text-accent' :
                      node.hardware.storageTier === 'nvme' ? 'text-primary' :
                      'text-warning'
                    }`}>{node.hardware.storageTier.toUpperCase()}</div>
                  </div>
                </div>
              </div>
            </TabsContent>
          </Tabs>
        </div>
      </SheetContent>
    </Sheet>
  )
}
