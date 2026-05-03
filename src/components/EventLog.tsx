import { SystemEvent } from '@/lib/types'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Badge } from '@/components/ui/badge'
import { Info, Warning, XCircle, CheckCircle } from '@phosphor-icons/react'

interface EventLogProps {
  events: SystemEvent[]
}

export function EventLog({ events }: EventLogProps) {
  const severityConfig = {
    info: {
      icon: Info,
      color: 'text-primary',
      badge: 'bg-primary/20 text-primary border-primary/30'
    },
    warning: {
      icon: Warning,
      color: 'text-[oklch(0.75_0.18_75)]',
      badge: 'bg-[oklch(0.75_0.18_75)]/20 text-[oklch(0.75_0.18_75)] border-[oklch(0.75_0.18_75)]/30'
    },
    error: {
      icon: XCircle,
      color: 'text-destructive',
      badge: 'bg-destructive/20 text-destructive border-destructive/30'
    },
    success: {
      icon: CheckCircle,
      color: 'text-accent',
      badge: 'bg-accent/20 text-accent border-accent/30'
    }
  }

  const formatTimestamp = (timestamp: number) => {
    const date = new Date(timestamp)
    return date.toLocaleTimeString('en-US', { 
      hour12: false,
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit'
    })
  }

  return (
    <Card className="h-full">
      <CardHeader>
        <CardTitle className="font-mono text-xl">System Events</CardTitle>
      </CardHeader>
      <CardContent>
        <ScrollArea className="h-[400px] pr-4">
          <div className="space-y-3">
            {events.length === 0 ? (
              <div className="text-center text-muted-foreground py-8">
                No events yet
              </div>
            ) : (
              events.map((event) => {
                const config = severityConfig[event.severity]
                const Icon = config.icon
                
                return (
                  <div
                    key={event.id}
                    className="flex items-start gap-3 p-3 rounded-md border border-border/50 bg-card/50 hover:bg-card transition-colors"
                  >
                    <div className={config.color}>
                      <Icon className="text-xl mt-0.5" />
                    </div>
                    <div className="flex-1 min-w-0 space-y-1">
                      <div className="flex items-start justify-between gap-2">
                        <p className="text-sm leading-relaxed break-words">
                          {event.message}
                        </p>
                        <Badge 
                          variant="outline" 
                          className={`${config.badge} shrink-0 text-xs`}
                        >
                          {event.severity}
                        </Badge>
                      </div>
                      <div className="text-xs text-muted-foreground font-mono">
                        {formatTimestamp(event.timestamp)}
                      </div>
                    </div>
                  </div>
                )
              })
            )}
          </div>
        </ScrollArea>
      </CardContent>
    </Card>
  )
}
