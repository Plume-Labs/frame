import { ValidationResult, ValidationError, ValidationWarning } from '@/lib/rackValidation'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { 
  Lightning, 
  Snowflake, 
  Warning, 
  CheckCircle, 
  XCircle,
  Ruler,
  Package
} from '@phosphor-icons/react'

interface RackValidationPanelProps {
  validation: ValidationResult
  rackId: string
}

export function RackValidationPanel({ validation, rackId }: RackValidationPanelProps) {
  const criticalErrors = validation.errors.filter(e => e.severity === 'critical')
  const regularErrors = validation.errors.filter(e => e.severity === 'error')
  const hasErrors = validation.errors.length > 0
  const hasWarnings = validation.warnings.length > 0

  const getErrorIcon = (type: ValidationError['type']) => {
    switch (type) {
      case 'power':
        return <Lightning className="w-4 h-4" weight="fill" />
      case 'cooling':
        return <Snowflake className="w-4 h-4" weight="fill" />
      case 'spacing':
        return <Ruler className="w-4 h-4" weight="fill" />
      case 'physical':
        return <Package className="w-4 h-4" weight="fill" />
      case 'capacity':
        return <Package className="w-4 h-4" weight="fill" />
    }
  }

  const getWarningIcon = (type: ValidationWarning['type']) => {
    switch (type) {
      case 'power':
        return <Lightning className="w-4 h-4" weight="fill" />
      case 'cooling':
        return <Snowflake className="w-4 h-4" weight="fill" />
      case 'thermal':
        return <Snowflake className="w-4 h-4" weight="fill" />
      case 'efficiency':
        return <Warning className="w-4 h-4" weight="fill" />
    }
  }

  if (!hasErrors && !hasWarnings) {
    return (
      <Alert className="border-accent bg-accent/10">
        <CheckCircle className="w-5 h-5 text-accent" weight="fill" />
        <AlertTitle className="font-mono text-accent">All Constraints Satisfied</AlertTitle>
        <AlertDescription className="font-mono text-sm text-muted-foreground">
          {rackId} meets all power, cooling, and physical requirements
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Card className={hasErrors ? 'border-destructive' : 'border-warning'}>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            {hasErrors ? (
              <XCircle className="w-5 h-5 text-destructive" weight="fill" />
            ) : (
              <Warning className="w-5 h-5 text-warning" weight="fill" />
            )}
            <CardTitle className="font-mono text-sm">
              Validation Results - {rackId}
            </CardTitle>
          </div>
          <div className="flex gap-2">
            {criticalErrors.length > 0 && (
              <Badge variant="destructive" className="font-mono">
                {criticalErrors.length} Critical
              </Badge>
            )}
            {regularErrors.length > 0 && (
              <Badge variant="destructive" className="font-mono">
                {regularErrors.length} Errors
              </Badge>
            )}
            {hasWarnings && (
              <Badge variant="outline" className="font-mono border-warning text-warning">
                {validation.warnings.length} Warnings
              </Badge>
            )}
          </div>
        </div>
        <CardDescription className="font-mono">
          {hasErrors ? 'Critical issues must be resolved before deployment' : 'Review warnings to optimize rack configuration'}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <ScrollArea className="h-[300px] pr-4">
          <div className="space-y-4">
            {criticalErrors.length > 0 && (
              <div className="space-y-2">
                <h4 className="text-sm font-mono font-semibold text-destructive flex items-center gap-2">
                  <XCircle className="w-4 h-4" weight="fill" />
                  Critical Errors
                </h4>
                {criticalErrors.map((error, idx) => (
                  <Alert key={idx} variant="destructive" className="font-mono">
                    <div className="flex items-start gap-2">
                      {getErrorIcon(error.type)}
                      <div className="flex-1">
                        <AlertTitle className="text-sm">{error.message}</AlertTitle>
                        {error.currentValue !== undefined && error.limitValue !== undefined && (
                          <AlertDescription className="text-xs mt-1">
                            Current: {error.currentValue.toFixed(1)} / Limit: {error.limitValue.toFixed(1)}
                          </AlertDescription>
                        )}
                        {error.affectedUnits && (
                          <AlertDescription className="text-xs mt-1">
                            Affected units: {error.affectedUnits.map(u => `U${u}`).join(', ')}
                          </AlertDescription>
                        )}
                      </div>
                    </div>
                  </Alert>
                ))}
              </div>
            )}

            {regularErrors.length > 0 && (
              <>
                {criticalErrors.length > 0 && <Separator />}
                <div className="space-y-2">
                  <h4 className="text-sm font-mono font-semibold text-destructive flex items-center gap-2">
                    <Warning className="w-4 h-4" weight="fill" />
                    Errors
                  </h4>
                  {regularErrors.map((error, idx) => (
                    <Alert key={idx} variant="destructive" className="font-mono">
                      <div className="flex items-start gap-2">
                        {getErrorIcon(error.type)}
                        <div className="flex-1">
                          <AlertTitle className="text-sm">{error.message}</AlertTitle>
                          {error.currentValue !== undefined && error.limitValue !== undefined && (
                            <AlertDescription className="text-xs mt-1">
                              Current: {error.currentValue.toFixed(1)} / Limit: {error.limitValue.toFixed(1)}
                            </AlertDescription>
                          )}
                          {error.affectedUnits && (
                            <AlertDescription className="text-xs mt-1">
                              Affected units: {error.affectedUnits.map(u => `U${u}`).join(', ')}
                            </AlertDescription>
                          )}
                        </div>
                      </div>
                    </Alert>
                  ))}
                </div>
              </>
            )}

            {hasWarnings && (
              <>
                {hasErrors && <Separator />}
                <div className="space-y-2">
                  <h4 className="text-sm font-mono font-semibold text-warning flex items-center gap-2">
                    <Warning className="w-4 h-4" weight="fill" />
                    Warnings
                  </h4>
                  {validation.warnings.map((warning, idx) => (
                    <Alert key={idx} className="border-warning bg-warning/5 font-mono">
                      <div className="flex items-start gap-2">
                        {getWarningIcon(warning.type)}
                        <div className="flex-1">
                          <AlertTitle className="text-sm text-warning">{warning.message}</AlertTitle>
                          <AlertDescription className="text-xs mt-1">
                            {warning.recommendation}
                          </AlertDescription>
                          {warning.currentValue !== undefined && warning.thresholdValue !== undefined && (
                            <AlertDescription className="text-xs mt-1">
                              Current: {warning.currentValue.toFixed(1)} / Threshold: {warning.thresholdValue.toFixed(1)}
                            </AlertDescription>
                          )}
                        </div>
                      </div>
                    </Alert>
                  ))}
                </div>
              </>
            )}
          </div>
        </ScrollArea>
      </CardContent>
    </Card>
  )
}
