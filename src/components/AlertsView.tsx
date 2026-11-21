import { useState } from 'react'
import { toast } from 'sonner'
import {
  ActiveAlert,
  AlertSilence,
  AlertsStatus,
  AlertSubscriptionHealth,
  RegistryAlert,
  alertRegistryPath,
  alertSubscriptionsPath,
  createFrameClient,
} from '@/lib/frame-sdk'
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
import { useI18n } from '@/i18n/context'

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
  const { t, locale } = useI18n()
  // Alertmanager's own API, not a Kubernetes object — nothing to watch.
  const { state, reload } = useLiveResource<AlertsStatus | null>(() => frame.cluster.alerts(), [], [], 30_000)
  const { state: silenceState, reload: reloadSilences } = useLiveResource<AlertSilence[]>(
    () => frame.cluster.silences(),
    [],
    [],
    30_000,
  )
  const { state: registryState } = useLiveResource<RegistryAlert[]>(
    () => frame.cluster.alertRegistry(),
    [],
    [alertRegistryPath()],
  )
  const { state: subsState } = useLiveResource<AlertSubscriptionHealth[]>(
    () => frame.cluster.alertSubscriptions(),
    [],
    [alertSubscriptionsPath()],
  )
  const [view, setView] = useState<'Firing' | 'Resolved'>('Firing')
  const registry = registryState.phase === 'ready' ? registryState.data : []
  const subscriptions = subsState.phase === 'ready' ? subsState.data : []
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
      toast.success(t('al.silenced', { name: silenceTarget.name }))
      setSilenceTarget(null)
      reloadAll()
    } catch (e) {
      toast.error(t('al.silenceFailed', { name: silenceTarget.name }), { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  async function expire(s: AlertSilence) {
    setExpiring(s.id)
    try {
      await frame.cluster.expireSilence(s.id)
      toast.success(t('al.lifted'), { description: s.matchers })
      reloadAll()
    } catch (e) {
      toast.error(t('al.liftFailed'), { description: e instanceof Error ? e.message : String(e) })
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
            {t('al.title')}
            <Button variant="outline" size="sm" className="ml-auto font-mono gap-1.5" onClick={reloadAll} disabled={state.phase === 'loading'}>
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        {data && (
          <CardContent className="flex flex-wrap items-center gap-3">
            <div className="mr-4">
              <div className="text-xs text-muted-foreground uppercase tracking-wide">{t('al.firing')}</div>
              <div className="font-mono font-bold text-2xl text-foreground">{data.alerts.length}</div>
            </div>
            {Object.entries(data.bySeverity)
              .sort()
              .map(([s, n]) => (
                <Badge key={s} variant="outline" className={`font-mono text-[11px] border-current ${TONE[s] ?? 'text-foreground'}`}>
                  {n} {s}
                </Badge>
              ))}
            <span className="text-[11px] text-muted-foreground font-mono">{t('al.sources')}</span>
          </CardContent>
        )}
      </Card>

      <LiveStates state={state} emptyLabel={t('al.empty')} />

      {data && data.alerts.length > 0 && (
        <Card>
          <CardContent className="pt-6 space-y-2">
            {data.alerts.map((a, i) => (
              <div key={`${a.name}-${a.namespace}-${i}`} className="flex flex-wrap items-center gap-2 rounded border border-border bg-secondary/30 px-3 py-2">
                <Badge variant="outline" className={`font-mono text-[10px] border-current ${TONE[a.severity] ?? 'text-foreground'}`}>{a.severity}</Badge>
                <span className="font-mono text-xs font-bold">{a.name}</span>
                {a.namespace && <span className="font-mono text-[10px] text-muted-foreground">{a.namespace}</span>}
                <span className="font-mono text-[10px] text-muted-foreground flex-1 truncate">{a.summary}</span>
                <span className="font-mono text-[10px] text-muted-foreground">{a.startsAt ? new Date(a.startsAt).toLocaleTimeString(locale) : ''}</span>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-6 font-mono text-[10px] gap-1"
                  onClick={() => setSilenceTarget(a)}
                >
                  <BellSlash size={10} />
                  {t('al.silence')}
                </Button>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader className="flex flex-row items-center gap-2">
          <CardTitle className="font-mono text-base">{t('al.registry')}</CardTitle>
          <div className="ml-auto flex gap-1">
            {(['Firing', 'Resolved'] as const).map((v) => (
              <Button key={v} size="sm" variant={view === v ? 'default' : 'outline'} onClick={() => setView(v)}>
                {v === 'Firing' ? t('al.active') : t('al.history')}
              </Button>
            ))}
          </div>
        </CardHeader>
        <CardContent className="space-y-2">
          {registryState.phase === 'error' ? (
            <p className="text-sm font-mono text-destructive">
              {t('al.registryError', { message: registryState.message })}
            </p>
          ) : registry.filter((a) => a.state === view).length === 0 ? (
            <p className="text-sm font-mono text-muted-foreground">
              {view === 'Firing' ? t('al.registryNoActive') : t('al.registryNoResolved')}
            </p>
          ) : (
            registry
              .filter((a) => a.state === view)
              .map((a) => (
                <div key={a.name} className="flex items-center gap-3 p-2 rounded border border-border font-mono text-xs">
                  <span className={TONE[a.severity] ?? TONE.none}>{a.alertName}</span>
                  <span className="text-muted-foreground">{a.namespace || t('al.cluster')}</span>
                  <span className="ml-auto flex gap-1">
                    {a.deliveries.map((d) => (
                      <Badge
                        key={d.subscription}
                        variant="outline"
                        className={d.delivered ? 'text-accent' : d.permanent ? 'text-destructive' : 'text-warning'}
                        title={d.lastError || undefined}
                      >
                        {d.subscription} {d.delivered ? '✓' : d.permanent ? '✗' : '…'}
                      </Badge>
                    ))}
                  </span>
                </div>
              ))
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-base">{t('al.subs')}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {subsState.phase === 'error' ? (
            <p className="text-sm font-mono text-destructive">
              {t('al.subsError', { message: subsState.message })}
            </p>
          ) : subscriptions.length === 0 ? (
            <p className="text-sm font-mono text-muted-foreground">{t('al.subsNone')}</p>
          ) : (
            subscriptions.map((s) => (
              <div key={s.name} className="flex items-center gap-3 p-2 rounded border border-border font-mono text-xs">
                <span className="font-bold">{s.name}</span>
                <span className="text-muted-foreground truncate">{s.url}</span>
                {s.paused && <Badge variant="outline">{t('al.paused')}</Badge>}
                <span className="ml-auto">{t('al.pending', { n: s.pending })}</span>
                <span className="text-muted-foreground">{s.lastSuccessAt ? t('al.lastOk', { when: s.lastSuccessAt }) : t('al.neverDelivered')}</span>
                {s.lastError && <span className="text-destructive">{s.lastError}</span>}
              </div>
            ))
          )}
        </CardContent>
      </Card>

      {silences.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="font-mono text-lg flex items-center gap-2">
              <BellSlash className="text-warning" />
              {t('al.silences', { n: silences.length })}
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
                  {t('al.until', { when: s.endsAt ? new Date(s.endsAt).toLocaleString(locale) : '—' })}
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
                  {expiring === s.id ? t('al.lifting') : t('al.lift')}
                </Button>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      <AlertDialog open={!!silenceTarget} onOpenChange={(open) => !open && setSilenceTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('al.confirmTitle', { name: silenceTarget?.name ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('al.confirmDesc', { n: silenceTarget ? Object.keys(silenceTarget.labels).length : 0 })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="space-y-1.5">
            <label className="text-xs text-muted-foreground font-mono">{t('al.duration')}</label>
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
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={doSilence} disabled={busy}>
              {busy ? t('al.silencing') : t('al.silence')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
