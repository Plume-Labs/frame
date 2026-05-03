import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { CapacityAlert } from '@/lib/types'
import { Warning, TrendUp, WarningCircle, Lightbulb } from '@phosphor-icons/react'
import { Badge } from '@/components/ui/badge'

interface CapacityPlanningDashboardProps {
  alerts: CapacityAlert[]
}

export function CapacityPlanningDashboard({ alerts }: CapacityPlanningDashboardProps) {
  const criticalAlerts = alerts.filter(a => a.severity === 'critical')
  const warningAlerts = alerts.filter(a => a.severity === 'warning')

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <Warning className="text-warning" size={24} weight="duotone" />
        <h2 className="text-2xl font-mono font-semibold text-foreground">
          Capacity Alerts
        </h2>
      </div>

      {alerts.length === 0 ? (
        <Card className="border-accent/20">
          <CardContent className="pt-6">
            <div className="flex items-center gap-3 text-accent">
              <TrendUp size={32} weight="duotone" />
              <div>
                <p className="font-semibold">All Systems Normal</p>
                <p className="text-sm text-muted-foreground">
                  No capacity concerns detected in the forecast period
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4">
          {criticalAlerts.length > 0 && (
            <Card className="border-destructive/50 bg-destructive/5">
              <CardHeader>
                <div className="flex items-center gap-2">
                  <WarningCircle className="text-destructive" size={20} weight="fill" />
                  <CardTitle className="text-lg">Critical Alerts</CardTitle>
                </div>
                <CardDescription>Immediate action required</CardDescription>
              </CardHeader>
              <CardContent className="space-y-3">
                {criticalAlerts.map((alert) => (
                  <AlertCard key={alert.id} alert={alert} />
                ))}
              </CardContent>
            </Card>
          )}

          {warningAlerts.length > 0 && (
            <Card className="border-warning/50 bg-warning/5">
              <CardHeader>
                <div className="flex items-center gap-2">
                  <Warning className="text-warning" size={20} weight="fill" />
                  <CardTitle className="text-lg">Warning Alerts</CardTitle>
                </div>
                <CardDescription>Plan capacity expansion</CardDescription>
              </CardHeader>
              <CardContent className="space-y-3">
                {warningAlerts.map((alert) => (
                  <AlertCard key={alert.id} alert={alert} />
                ))}
              </CardContent>
            </Card>
          )}
        </div>
      )}
    </div>
  )
}

function AlertCard({ alert }: { alert: CapacityAlert }) {
  return (
    <div className="space-y-2 p-3 rounded-lg bg-card border border-border">
      <div className="flex items-start justify-between gap-2">
        <div className="flex-1">
          <div className="flex items-center gap-2 mb-1">
            <Badge
              variant={alert.severity === 'critical' ? 'destructive' : 'secondary'}
              className="font-mono text-xs"
            >
              {alert.resource.toUpperCase()}
            </Badge>
            {alert.estimatedDaysUntilFull > 0 && (
              <span className="text-xs text-muted-foreground font-mono">
                {alert.estimatedDaysUntilFull}d
              </span>
            )}
          </div>
          <p className="text-sm font-medium">{alert.message}</p>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-2 text-xs">
        <div>
          <span className="text-muted-foreground">Current:</span>{' '}
          <span className="font-mono font-semibold">{alert.currentUsage.toFixed(1)}%</span>
        </div>
        <div>
          <span className="text-muted-foreground">Projected:</span>{' '}
          <span className="font-mono font-semibold">{alert.projectedUsage.toFixed(1)}%</span>
        </div>
      </div>

      <div className="flex items-start gap-2 pt-2 border-t border-border/50">
        <Lightbulb className="text-primary flex-shrink-0 mt-0.5" size={14} weight="fill" />
        <p className="text-xs text-muted-foreground">{alert.recommendation}</p>
      </div>
    </div>
  )
}
