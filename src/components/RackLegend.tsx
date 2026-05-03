import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Cpu, Database, WifiHigh, Ruler } from '@phosphor-icons/react'

export function RackLegend() {
  return (
    <Card className="bg-muted/30">
      <CardHeader className="pb-3">
        <CardTitle className="text-sm font-mono flex items-center gap-2">
          <Ruler className="w-4 h-4" weight="duotone" />
          Rack Unit Legend
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="space-y-2">
          <div className="text-xs text-muted-foreground font-mono mb-1.5">Device Types</div>
          <div className="grid grid-cols-3 gap-2">
            <div className="flex items-center gap-2 text-xs">
              <Cpu className="w-4 h-4 text-primary" weight="duotone" />
              <span className="font-mono">Server</span>
            </div>
            <div className="flex items-center gap-2 text-xs">
              <Database className="w-4 h-4 text-primary" weight="duotone" />
              <span className="font-mono">Storage</span>
            </div>
            <div className="flex items-center gap-2 text-xs">
              <WifiHigh className="w-4 h-4 text-primary" weight="duotone" />
              <span className="font-mono">Network</span>
            </div>
          </div>
        </div>

        <div className="space-y-2">
          <div className="text-xs text-muted-foreground font-mono mb-1.5">Status</div>
          <div className="grid grid-cols-2 gap-2">
            <div className="flex items-center gap-2 text-xs">
              <div className="w-3 h-3 rounded border-2 border-primary bg-primary/20" />
              <span className="font-mono">Online</span>
            </div>
            <div className="flex items-center gap-2 text-xs">
              <div className="w-3 h-3 rounded border-2 border-warning bg-warning/20" />
              <span className="font-mono">Degraded</span>
            </div>
            <div className="flex items-center gap-2 text-xs">
              <div className="w-3 h-3 rounded border-2 border-destructive bg-destructive/20" />
              <span className="font-mono">Offline</span>
            </div>
            <div className="flex items-center gap-2 text-xs">
              <div className="w-3 h-3 rounded border-2 border-accent bg-accent/20" />
              <span className="font-mono">Provisioning</span>
            </div>
          </div>
        </div>

        <div className="space-y-2">
          <div className="text-xs text-muted-foreground font-mono mb-1.5">Unit Sizes</div>
          <div className="space-y-1.5">
            <div className="flex items-center justify-between text-xs">
              <span className="font-mono">1U</span>
              <Badge variant="outline" className="font-mono text-[10px] h-5">
                Standard Server
              </Badge>
            </div>
            <div className="flex items-center justify-between text-xs">
              <span className="font-mono">2U</span>
              <Badge variant="outline" className="font-mono text-[10px] h-5">
                Storage Array
              </Badge>
            </div>
            <div className="flex items-center justify-between text-xs">
              <span className="font-mono">4U</span>
              <Badge variant="outline" className="font-mono text-[10px] h-5">
                High-Density
              </Badge>
            </div>
          </div>
        </div>

        <div className="pt-2 border-t border-border">
          <div className="text-[10px] text-muted-foreground font-mono">
            Standard rack: 42U total height
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
