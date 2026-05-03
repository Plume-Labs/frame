import { Anomaly } from '@/lib/types'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Warning, TrendUp, TrendDown, ChartLine, Lightning, Waveform } from '@phosphor-icons/react'
import { motion, AnimatePresence } from 'framer-motion'

interface AnomalyAlertsProps {
  anomalies: Anomaly[]
}

const severityColors: Record<Anomaly['severity'], string> = {
  critical: 'bg-destructive text-destructive-foreground',
  high: 'bg-warning text-warning-foreground',
  medium: 'bg-accent text-accent-foreground',
  low: 'bg-secondary text-secondary-foreground'
}

const typeIcons: Record<Anomaly['type'], typeof Warning> = {
  spike: TrendUp,
  drop: TrendDown,
  pattern_deviation: ChartLine,
  sustained_high: Warning,
  oscillation: Waveform
}

const resourceColors: Record<Anomaly['resource'], string> = {
  cpu: 'text-primary',
  memory: 'text-accent',
  storage: 'text-chart-3',
  network: 'text-chart-2'
}

export function AnomalyAlerts({ anomalies }: AnomalyAlertsProps) {
  const criticalCount = anomalies.filter(a => a.severity === 'critical').length
  const highCount = anomalies.filter(a => a.severity === 'high').length
  const mediumCount = anomalies.filter(a => a.severity === 'medium').length
  const lowCount = anomalies.filter(a => a.severity === 'low').length

  return (
    <Card className="border-border/50">
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardTitle className="flex items-center gap-2 font-mono text-foreground">
              <Lightning className="text-warning" weight="fill" />
              Anomaly Detection
            </CardTitle>
            <CardDescription>Real-time behavior pattern analysis</CardDescription>
          </div>
          <div className="flex gap-2">
            {criticalCount > 0 && (
              <Badge variant="destructive" className="font-mono">
                {criticalCount} Critical
              </Badge>
            )}
            {highCount > 0 && (
              <Badge className="bg-warning text-warning-foreground font-mono">
                {highCount} High
              </Badge>
            )}
            {mediumCount > 0 && (
              <Badge variant="secondary" className="font-mono">
                {mediumCount} Medium
              </Badge>
            )}
            {lowCount > 0 && (
              <Badge variant="outline" className="font-mono">
                {lowCount} Low
              </Badge>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {anomalies.length === 0 ? (
          <div className="text-center py-12">
            <ChartLine className="mx-auto mb-4 text-muted-foreground" size={48} weight="light" />
            <p className="text-muted-foreground font-mono">
              No anomalies detected. All systems operating within normal parameters.
            </p>
          </div>
        ) : (
          <ScrollArea className="h-[400px] pr-4">
            <AnimatePresence mode="popLayout">
              <div className="space-y-3">
                {anomalies.map((anomaly, index) => {
                  const Icon = typeIcons[anomaly.type]
                  return (
                    <motion.div
                      key={anomaly.id}
                      initial={{ opacity: 0, y: 20 }}
                      animate={{ opacity: 1, y: 0 }}
                      exit={{ opacity: 0, x: -100 }}
                      transition={{ duration: 0.3, delay: index * 0.05 }}
                    >
                      <Card className={`border-l-4 ${
                        anomaly.severity === 'critical' ? 'border-l-destructive' :
                        anomaly.severity === 'high' ? 'border-l-warning' :
                        anomaly.severity === 'medium' ? 'border-l-accent' :
                        'border-l-secondary'
                      }`}>
                        <CardContent className="p-4">
                          <div className="flex items-start justify-between mb-3">
                            <div className="flex items-center gap-3">
                              <div className={`p-2 rounded-lg bg-card ${resourceColors[anomaly.resource]}`}>
                                <Icon size={20} weight="bold" />
                              </div>
                              <div>
                                <div className="flex items-center gap-2">
                                  <Badge className={severityColors[anomaly.severity]}>
                                    {anomaly.severity.toUpperCase()}
                                  </Badge>
                                  <Badge variant="outline" className="font-mono">
                                    {anomaly.type.replace('_', ' ').toUpperCase()}
                                  </Badge>
                                  <span className={`font-mono font-bold ${resourceColors[anomaly.resource]}`}>
                                    {anomaly.resource.toUpperCase()}
                                  </span>
                                </div>
                                {anomaly.nodeId && (
                                  <p className="text-xs text-muted-foreground font-mono mt-1">
                                    Node: {anomaly.nodeId}
                                  </p>
                                )}
                              </div>
                            </div>
                            <div className="text-right">
                              <p className="text-xs text-muted-foreground font-mono">
                                {new Date(anomaly.timestamp).toLocaleTimeString()}
                              </p>
                              <p className="text-xs text-muted-foreground font-mono">
                                Confidence: {anomaly.confidence.toFixed(0)}%
                              </p>
                            </div>
                          </div>
                          
                          <p className="text-sm text-foreground mb-2 font-mono">
                            {anomaly.description}
                          </p>
                          
                          <div className="grid grid-cols-2 gap-2 mb-3 text-xs font-mono">
                            <div className="bg-muted/30 p-2 rounded">
                              <span className="text-muted-foreground">Current:</span>
                              <span className="ml-2 font-bold text-foreground">
                                {anomaly.value.toFixed(1)}%
                              </span>
                            </div>
                            <div className="bg-muted/30 p-2 rounded">
                              <span className="text-muted-foreground">Expected:</span>
                              <span className="ml-2 font-bold text-foreground">
                                {anomaly.expectedValue.toFixed(1)}%
                              </span>
                            </div>
                            <div className="bg-muted/30 p-2 rounded">
                              <span className="text-muted-foreground">Deviation:</span>
                              <span className="ml-2 font-bold text-foreground">
                                {anomaly.deviation.toFixed(2)}σ
                              </span>
                            </div>
                            {anomaly.duration !== undefined && (
                              <div className="bg-muted/30 p-2 rounded">
                                <span className="text-muted-foreground">Duration:</span>
                                <span className="ml-2 font-bold text-foreground">
                                  {anomaly.duration} intervals
                                </span>
                              </div>
                            )}
                          </div>
                          
                          <div className="bg-primary/10 border border-primary/20 rounded p-3">
                            <p className="text-xs font-semibold text-primary mb-1 font-mono">
                              RECOMMENDATION:
                            </p>
                            <p className="text-xs text-foreground">
                              {anomaly.recommendation}
                            </p>
                          </div>
                        </CardContent>
                      </Card>
                    </motion.div>
                  )
                })}
              </div>
            </AnimatePresence>
          </ScrollArea>
        )}
      </CardContent>
    </Card>
  )
}
