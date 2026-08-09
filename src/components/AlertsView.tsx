import { useState } from 'react'
import { toast } from 'sonner'
import { ActiveAlert, AlertSilence, AlertsStatus, createFrameClient } from '@/lib/frame-sdk'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { useLiveResource } from '@/hooks/useLiveResource'
import { LiveStates } from '@/components/LiveStates'
import { Bell, ArrowClockwise, BellSlash, BellRinging } from '@phosphor-icons/react'

const frame = createFrameClient()

const DURATIONS = [
  { label: '15m', minutes: 15 },
  { label: '1h', minutes: 60 },
  { label: '4h', minutes: 240 },
  { label: '24h', minutes: 1440 },
]

const TONE: Record<string, string> = {
  critical: 'text-destructive',
  warning: 'text-warning',
  info: 'text-accent',
  none: 'text-muted-foreground',
}

export function AlertsView() {
  // Alertmanager's own API, not a Kubernetes object — nothing to watch.
  const { state, reload } = useLiveResource<AlertsStatus | null>(() => frame.cluster.alerts(), [], [], 30_000)
  const { state: silenceState, reload: reloadSilences } = useLiveResource<AlertSilence[]>(
    () => frame.cluster.silences(),
    [],
    [],
    30_000,
  )
  const data = state.phase === 'ready' ? state.data : null
  const silences = silenceState.phase === 'ready' ? silenceState.data : []
  const [silenceTarget, setSilenceTarget] = useState<ActiveAlert | null>(null)
  const [duration, setDuration] = useState(60)
  const [busy, setBusy] = useState(false)
  const [expiring, setExpiring] = useState<string | null>(null)

  function reloadAll() {
    reload()
    reloadSilences()
  }

  async function doSilence() {
    if (!silenceTarget) return
    setBusy(true)
    try {
      await frame.cluster.silenceAlert(silenceTarget, duration, 'frame-ui')
      toast.success(`${silenceTarget.name} silenced`)
      setSilenceTarget(null)
      reloadAll()
    } catch (e) {
      toast.error(`Failed to silence ${silenceTarget.name}`, { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  async function expire(s: AlertSilence) {
    setExpiring(s.id)
    try {
      await frame.cluster.expireSilence(s.id)
      toast.success('Silence lifted', { description: s.matchers })
      reloadAll()
    } catch (e) {
      toast.error('Failed to lift silence', { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setExpiring(null)
    }
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Bell className="text-primary" />
            Active alerts (Alertmanager)
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reloadAll} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        {data && (
          <CardContent className="flex flex-wrap items-center gap-3">
            <div className="mr-4">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">Firing</div>
              <div className="font-mono font-bold text-2xl text-foreground">{data.alerts.length}</div>
            </div>
            {Object.entries(data.bySeverity)
              .sort()
              .map(([s, n]) => (
                <Badge key={s} variant="outline" className={`font-mono text-[11px] border-current ${TONE[s] ?? 'text-foreground'}`}>
                  {n} {s}
                </Badge>
              ))}
            <span className="text-[11px] text-muted-foreground font-mono">Prometheus rules + routed Falco detections</span>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel="No active alerts (or Alertmanager not deployed)." />

      {data && data.alerts.length > 0 && (
        <Card>
          <CardContent className="pt-6 space-y-2">
            {data.alerts.map((a, i) => (
              <div key={`${a.name}-${a.namespace}-${i}`} className="flex flex-wrap items-center gap-2 rounded border border-border bg-secondary/30 px-3 py-2">
                <Badge variant="outline" className={`font-mono text-[10px] border-current ${TONE[a.severity] ?? 'text-foreground'}`}>{a.severity}</Badge>
                <span className="font-mono text-xs font-bold">{a.name}</span>
                {a.namespace && <span className="font-mono text-[10px] text-muted-foreground">{a.namespace}</span>}
                <span className="font-mono text-[10px] text-muted-foreground flex-1 truncate">{a.summary}</span>
                <span className="font-mono text-[10px] text-muted-foreground">{a.startsAt ? new Date(a.startsAt).toLocaleTimeString() : ''}</span>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-6 font-mono text-[10px] gap-1"
                  onClick={() => setSilenceTarget(a)}
                >
                  <BellSlash size={10} />
                  Silence
                </Button>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      {silences.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg flex items-center gap-2">
              <BellSlash className="text-warning" />
              Silences ({silences.length})
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {silences.map((s) => (
              <div
                key={s.id}
                className="flex flex-wrap items-center gap-2 rounded border border-border bg-secondary/30 px-3 py-2"
              >
                <Badge
                  variant="outline"
                  className={`font-mono text-[10px] border-current ${
                    s.state === 'active' ? 'text-warning' : 'text-muted-foreground'
                  }`}
                >
                  {s.state}
                </Badge>
                <span className="font-mono text-[11px] flex-1 truncate">{s.matchers}</span>
                <span className="font-mono text-[10px] text-muted-foreground">
                  until {s.endsAt ? new Date(s.endsAt).toLocaleString() : '—'}
                </span>
                <span className="font-mono text-[10px] text-muted-foreground">{s.createdBy}</span>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-6 font-mono text-[10px] gap-1"
                  disabled={expiring === s.id}
                  onClick={() => expire(s)}
                >
                  <BellRinging size={10} />
                  {expiring === s.id ? 'Lifting…' : 'Lift'}
                </Button>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      <AlertDialog open={!!silenceTarget} onOpenChange={(open) => !open && setSilenceTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Silence {silenceTarget?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Matches on every label of this alert instance ({silenceTarget ? Object.keys(silenceTarget.labels).length : 0} labels) —
              won't suppress other alerts with the same name but different labels.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground font-mono">Duration</label>
            <Select value={String(duration)} onValueChange={(v) => setDuration(Number(v))}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {DURATIONS.map((d) => (
                  <SelectItem key={d.minutes} value={String(d.minutes)}>
                    {d.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={doSilence} disabled={busy}>
              {busy ? 'Silencing…' : 'Silence'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
