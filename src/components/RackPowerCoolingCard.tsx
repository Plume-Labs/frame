import { RackPowerCooling } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { 
  Lightning, 
  ThermometerSimple, 
  Fan,
  Warning,
  Info,
  Fire
} from '@phosphor-icons/react'
import { Alert, AlertDescription } from '@/components/ui/alert'

interface RackPowerCoolingCardProps {
  metrics: RackPowerCooling
}

export function RackPowerCoolingCard({ metrics }: RackPowerCoolingCardProps) {
  const { power, cooling, thermalLoad, powerDensity, alerts } = metrics

  const powerUtilization = (power.currentDraw / power.maxCapacity) * 100

  const getPowerColor = () => {
    if (powerUtilization > 85) return 'text-destructive'
    if (powerUtilization > 75) return 'text-warning'
    return 'text-primary'
  }

  const getTempColor = (temp: number) => {
    if (temp > 35) return 'text-destructive'
    if (temp > 32) return 'text-warning'
    return 'text-primary'
  }

  const getEfficiencyColor = (efficiency: number) => {
    if (efficiency < 0.85) return 'text-destructive'
    if (efficiency < 0.90) return 'text-warning'
    return 'text-accent'
  }

  const getPUEColor = (pue: number) => {
    if (pue > 1.5) return 'text-destructive'
    if (pue > 1.3) return 'text-warning'
    return 'text-accent'
  }

  const criticalAlerts = alerts.filter(a => a.severity === 'critical')
  const warningAlerts = alerts.filter(a => a.severity === 'warning')

  return (
    <Card className="border-2">
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle className="text-lg font-mono flex items-center gap-2">
            <Lightning className="w-5 h-5 text-primary" weight="duotone" />
            Power & Cooling
          </CardTitle>
          {(criticalAlerts.length > 0 || warningAlerts.length > 0) && (
            <Badge 
              variant="outline" 
              className={
                criticalAlerts.length > 0 
                  ? 'bg-destructive/20 text-destructive border-destructive'
                  : 'bg-warning/20 text-warning border-warning'
              }
            >
              <Warning className="w-3 h-3 mr-1" weight="fill" />
              {criticalAlerts.length + warningAlerts.length} Alert{alerts.length !== 1 ? 's' : ''}
            </Badge>
          )}
        </div>
      </CardHeader>

      <CardContent className="space-y-6">
        <div className="space-y-4">
          <div className="flex items-center gap-2">
            <Lightning className="w-4 h-4 text-primary" weight="duotone" />
            <h3 className="font-mono font-semibold text-sm">Power Metrics</h3>
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Current Draw</div>
              <div className={`text-xl font-mono font-bold ${getPowerColor()}`}>
                {power.currentDraw}W
              </div>
              <Progress value={powerUtilization} className="h-1.5" />
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Max Capacity</div>
              <div className="text-xl font-mono font-bold text-foreground">
                {power.maxCapacity}W
              </div>
              <div className="text-xs text-muted-foreground font-mono">
                {powerUtilization.toFixed(1)}% utilized
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Efficiency</div>
              <div className={`text-lg font-mono font-bold ${getEfficiencyColor(power.efficiency)}`}>
                {(power.efficiency * 100).toFixed(1)}%
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">PUE</div>
              <div className={`text-lg font-mono font-bold ${getPUEColor(power.powerUsageEffectiveness)}`}>
                {power.powerUsageEffectiveness.toFixed(2)}
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Peak Draw</div>
              <div className="text-sm font-mono text-foreground">
                {power.peakDraw}W
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Power Density</div>
              <div className="text-sm font-mono text-foreground">
                {powerDensity}W/U
              </div>
            </div>
          </div>
        </div>

        <div className="h-px bg-border" />

        <div className="space-y-4">
          <div className="flex items-center gap-2">
            <ThermometerSimple className="w-4 h-4 text-primary" weight="duotone" />
            <h3 className="font-mono font-semibold text-sm">Cooling Metrics</h3>
          </div>

          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Inlet Temp</div>
              <div className="text-lg font-mono font-bold text-primary">
                {cooling.inletTemp}°C
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Outlet Temp</div>
              <div className={`text-lg font-mono font-bold ${getTempColor(cooling.outletTemp)}`}>
                {cooling.outletTemp}°C
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Ambient Temp</div>
              <div className="text-lg font-mono font-bold text-foreground">
                {cooling.ambientTemp}°C
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Delta T</div>
              <div className={`text-lg font-mono font-bold ${cooling.deltaT > 15 ? 'text-warning' : 'text-accent'}`}>
                {cooling.deltaT}°C
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono flex items-center gap-1">
                <Fan className="w-3 h-3" />
                Fan Speed
              </div>
              <div className="text-lg font-mono font-bold text-foreground">
                {cooling.fanSpeed.toFixed(0)}%
              </div>
              <Progress value={cooling.fanSpeed} className="h-1.5" />
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Airflow</div>
              <div className="text-lg font-mono font-bold text-foreground">
                {cooling.airflowCFM} CFM
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono flex items-center gap-1">
                <Fire className="w-3 h-3" />
                Thermal Load
              </div>
              <div className="text-sm font-mono text-foreground">
                {thermalLoad.toLocaleString()} BTU/hr
              </div>
            </div>

            <div className="space-y-1">
              <div className="text-xs text-muted-foreground font-mono">Cooling Efficiency</div>
              <div className={`text-sm font-mono font-bold ${getEfficiencyColor(cooling.coolingEfficiency)}`}>
                {(cooling.coolingEfficiency * 100).toFixed(1)}%
              </div>
            </div>
          </div>
        </div>

        {alerts.length > 0 && (
          <>
            <div className="h-px bg-border" />
            
            <div className="space-y-2">
              {alerts.map((alert) => (
                <Alert 
                  key={alert.id}
                  variant={alert.severity === 'critical' ? 'destructive' : 'default'}
                  className={
                    alert.severity === 'critical' 
                      ? ''
                      : alert.severity === 'warning'
                      ? 'border-warning/50 text-warning-foreground bg-warning/10'
                      : ''
                  }
                >
                  {alert.severity === 'critical' ? (
                    <Warning className="h-4 w-4" weight="fill" />
                  ) : (
                    <Info className="h-4 w-4" weight="duotone" />
                  )}
                  <AlertDescription className="text-xs font-mono">
                    {alert.message}
                  </AlertDescription>
                </Alert>
              ))}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
