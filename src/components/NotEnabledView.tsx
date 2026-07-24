import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Info } from '@phosphor-icons/react'

/**
 * Honest placeholder for a tuning capability that is real-but-not-active on this
 * cluster (rather than a simulated dashboard). States the real reason + how to
 * turn it on.
 */
export function NotEnabledView({ title, reason, enable }: { title: string; reason: string; enable: string }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-xl flex items-center gap-2">
          <Info className="text-primary" />
          {title}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        <p className="text-muted-foreground">{reason}</p>
        <div className="rounded border border-border bg-muted/40 p-3 font-mono text-xs text-foreground">{enable}</div>
      </CardContent>
    </Card>
  )
}
