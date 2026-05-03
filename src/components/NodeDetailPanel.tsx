import { ClusterNode } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Progress } from '@/components/ui/progress'
import { Separator } from '@/components/ui/separator'
import { Badge } from '@/components/ui/badge'
import { Cpu, HardDrives, Network, Database } from '@phosphor-icons/react'
import { formatUptime } from '@/lib/cluster'
import { useIsMobile } from '@/hooks/use-mobile'

interface NodeDetailPanelProps {
  node: ClusterNode | null
  open: boolean
  onClose: () => void
}

export function NodeDetailPanel({ node, open, onClose }: NodeDetailPanelProps) {
  const isMobile = useIsMobile()

  if (!node) return null

  const statusVariants = {
    online: 'bg-accent text-accent-foreground',
    degraded: 'bg-[oklch(0.75_0.18_75)] text-[oklch(0.15_0.02_240)]',
    offline: 'bg-destructive text-destructive-foreground',
    provisioning: 'bg-primary text-primary-foreground'
  }

  const metricItems = [
    { icon: Cpu, label: 'CPU Usage', value: node.metrics.cpu, unit: '%' },
    { icon: HardDrives, label: 'Memory', value: node.metrics.memory, unit: '%' },
    { icon: Database, label: 'Storage', value: node.metrics.storage, unit: '%' },
    { icon: Network, label: 'Network', value: node.metrics.network, unit: ' Mbps' }
  ]

  return (
    <Sheet open={open} onOpenChange={(isOpen) => !isOpen && onClose()}>
      <SheetContent side={isMobile ? 'bottom' : 'right'} className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle className="font-mono text-2xl flex items-center justify-between">
            {node.name}
            <Badge className={statusVariants[node.status]}>
              {node.status}
            </Badge>
          </SheetTitle>
        </SheetHeader>
        
        <div className="mt-6 space-y-6">
          <div className="space-y-2">
            <div className="text-sm text-muted-foreground">Node ID</div>
            <div className="font-mono text-lg">{node.id}</div>
          </div>

          <Separator />

          <div className="space-y-2">
            <div className="text-sm text-muted-foreground">Uptime</div>
            <div className="font-mono text-lg">{formatUptime(node.uptime)}</div>
          </div>

          <Separator />

          <div className="space-y-4">
            <div className="text-sm font-semibold">Resource Metrics</div>
            {metricItems.map(({ icon: Icon, label, value, unit }) => (
              <div key={label} className="space-y-2">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <Icon className="text-primary" />
                    <span className="text-sm">{label}</span>
                  </div>
                  <span className="font-mono text-sm font-semibold">
                    {unit === ' Mbps' ? value.toFixed(0) : value.toFixed(1)}{unit}
                  </span>
                </div>
                <Progress 
                  value={unit === ' Mbps' ? Math.min((value / 10000) * 100, 100) : value} 
                  className="h-2"
                />
              </div>
            ))}
          </div>

          <Separator />

          <div className="space-y-2">
            <div className="text-sm text-muted-foreground">Last Seen</div>
            <div className="font-mono text-sm">
              {new Date(node.lastSeen).toLocaleString()}
            </div>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}
