import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { CapacityPlan } from '@/lib/types'
import { HardDrives, TrendUp, CurrencyDollar, Calendar } from '@phosphor-icons/react'
import { Progress } from '@/components/ui/progress'
import { Separator } from '@/components/ui/separator'

interface CapacityPlanCardProps {
  plan: CapacityPlan
}

export function CapacityPlanCard({ plan }: CapacityPlanCardProps) {
  const formatNumber = (num: number) => num.toLocaleString()
  const formatCurrency = (num: number) => `$${num.toLocaleString()}`

  const cpuIncrease = ((plan.recommendedCapacity.cpu - plan.currentCapacity.cpu) / plan.currentCapacity.cpu) * 100
  const memoryIncrease = ((plan.recommendedCapacity.memory - plan.currentCapacity.memory) / plan.currentCapacity.memory) * 100
  const storageIncrease = ((plan.recommendedCapacity.storage - plan.currentCapacity.storage) / plan.currentCapacity.storage) * 100

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center gap-2">
          <HardDrives className="text-primary" size={20} weight="duotone" />
          <CardTitle className="font-mono">Capacity Expansion Plan</CardTitle>
        </div>
        <CardDescription>Recommended infrastructure scaling for {plan.timeframe}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="grid gap-4">
          <div className="space-y-2">
            <div className="flex justify-between items-center">
              <span className="text-sm text-muted-foreground">CPU Cores</span>
              <span className="text-xs font-mono text-primary">
                {cpuIncrease > 0 ? `+${cpuIncrease.toFixed(1)}%` : 'No change'}
              </span>
            </div>
            <div className="flex justify-between items-center">
              <span className="font-mono text-xs">{formatNumber(plan.currentCapacity.cpu)}</span>
              <span className="text-xs text-muted-foreground">→</span>
              <span className="font-mono text-xs font-semibold">{formatNumber(plan.recommendedCapacity.cpu)}</span>
            </div>
            <Progress value={(plan.currentCapacity.cpu / plan.recommendedCapacity.cpu) * 100} className="h-2" />
          </div>

          <div className="space-y-2">
            <div className="flex justify-between items-center">
              <span className="text-sm text-muted-foreground">Memory (GB)</span>
              <span className="text-xs font-mono text-primary">
                {memoryIncrease > 0 ? `+${memoryIncrease.toFixed(1)}%` : 'No change'}
              </span>
            </div>
            <div className="flex justify-between items-center">
              <span className="font-mono text-xs">{formatNumber(plan.currentCapacity.memory)}</span>
              <span className="text-xs text-muted-foreground">→</span>
              <span className="font-mono text-xs font-semibold">{formatNumber(plan.recommendedCapacity.memory)}</span>
            </div>
            <Progress value={(plan.currentCapacity.memory / plan.recommendedCapacity.memory) * 100} className="h-2" />
          </div>

          <div className="space-y-2">
            <div className="flex justify-between items-center">
              <span className="text-sm text-muted-foreground">Storage (GB)</span>
              <span className="text-xs font-mono text-primary">
                {storageIncrease > 0 ? `+${storageIncrease.toFixed(1)}%` : 'No change'}
              </span>
            </div>
            <div className="flex justify-between items-center">
              <span className="font-mono text-xs">{formatNumber(plan.currentCapacity.storage)}</span>
              <span className="text-xs text-muted-foreground">→</span>
              <span className="font-mono text-xs font-semibold">{formatNumber(plan.recommendedCapacity.storage)}</span>
            </div>
            <Progress value={(plan.currentCapacity.storage / plan.recommendedCapacity.storage) * 100} className="h-2" />
          </div>
        </div>

        <Separator />

        <div className="grid grid-cols-2 gap-4">
          <div className="space-y-1">
            <div className="flex items-center gap-2 text-muted-foreground">
              <TrendUp size={14} />
              <span className="text-xs">Growth Rate</span>
            </div>
            <div className="space-y-1">
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">CPU:</span>
                <span className="font-mono">{plan.growthRate.cpu.toFixed(2)}%/mo</span>
              </div>
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Mem:</span>
                <span className="font-mono">{plan.growthRate.memory.toFixed(2)}%/mo</span>
              </div>
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Stor:</span>
                <span className="font-mono">{plan.growthRate.storage.toFixed(2)}%/mo</span>
              </div>
            </div>
          </div>

          <div className="space-y-1">
            <div className="flex items-center gap-2 text-muted-foreground">
              <Calendar size={14} />
              <span className="text-xs">Timeline</span>
            </div>
            <div className="space-y-1">
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Period:</span>
                <span className="font-mono">{plan.timeframe}</span>
              </div>
              <div className="flex justify-between text-xs">
                <span className="text-muted-foreground">Nodes:</span>
                <span className="font-mono font-semibold text-primary">+{plan.nodesRequired}</span>
              </div>
            </div>
          </div>
        </div>

        <Separator />

        <div className="flex items-center justify-between p-3 rounded-lg bg-primary/10 border border-primary/20">
          <div className="flex items-center gap-2">
            <CurrencyDollar className="text-primary" size={20} weight="duotone" />
            <span className="text-sm font-medium">Estimated Cost</span>
          </div>
          <span className="text-lg font-mono font-bold text-primary">
            {formatCurrency(plan.estimatedCost)}
          </span>
        </div>

        {plan.nodesRequired === 0 && (
          <div className="text-center p-3 rounded-lg bg-accent/10 border border-accent/20">
            <p className="text-sm text-accent font-medium">Current capacity is sufficient</p>
            <p className="text-xs text-muted-foreground mt-1">No immediate expansion required</p>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
