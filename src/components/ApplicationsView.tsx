import { useState } from 'react'
import { toast } from 'sonner'
import { Application, AppComponent, APPLICATION_WATCH_PATHS, createFrameClient } from '@/lib/frame-sdk'
import type { WorkloadPod } from '@/lib/workloads'
import { useI18n } from '@/i18n/context'
import { richText } from '@/i18n/rich'
import type { MessageKey } from '@/i18n'
import { LogsTab } from '@/components/workloads/LogsTab'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useLiveResource } from '@/hooks/useLiveResource'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Button } from '@/components/ui/button'
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
import { TONE_TEXT, Tone } from '@/lib/thresholds'
import {
  Cube,
  ArrowClockwise,
  Warning,
  CheckCircle,
  Package,
  ArrowsClockwise,
  Minus,
  Plus,
  FileText,
} from '@phosphor-icons/react'

const frame = createFrameClient()

const HEALTH_TONE: Record<Application['health'], Tone> = {
  healthy: 'accent',
  degraded: 'warning',
  down: 'destructive',
}

const HEALTH_LABEL: Record<Application['health'], MessageKey> = {
  healthy: 'apps.health.healthy',
  degraded: 'apps.health.degraded',
  down: 'apps.health.down',
}

/**
 * Watched, not polled, and taken from the SDK rather than restated here: the
 * paths an application is assembled from are the paths this screen has to
 * watch, and APPLICATION_WATCH_PATHS is the one place that pair is written
 * down. Restating them would let the screen silently stop tracking a kind the
 * SDK started reading.
 *
 * Scaling and restarting write to these same collections, so the operator's
 * own action refreshes the screen; `reload` stays wired to the actions
 * anyway, because a watch is a change signal and not a delivery guarantee.
 */
const WATCHED = [...APPLICATION_WATCH_PATHS]

export function ApplicationsView() {
  const { t } = useI18n()
  const { state, reload } = useLiveResource<Application[]>(() => frame.apps.list(), [], WATCHED)

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Package className="text-primary" />
            {t('apps.title')}
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={() => reload()}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            {richText(t('apps.desc'), { label: <span className="font-mono">app.kubernetes.io/instance</span> })}
          </p>
        </CardContent>
      </Card>

      {state.phase === 'loading' && (
        <Card>
          <CardContent className="py-10 text-center font-mono text-sm text-muted-foreground">
            {t('apps.loading')}
          </CardContent>
        </Card>
      )}

      {state.phase === 'error' && (
        <Card className="border-destructive/40">
          <CardContent className="py-8 space-y-3 text-center">
            <Warning className="mx-auto text-destructive" size={28} />
            <div className="font-mono text-sm text-foreground">{t('apps.unreachable')}</div>
            <div className="text-xs text-muted-foreground max-w-md mx-auto">{state.message}</div>
            <div className="text-xs text-muted-foreground">
              {richText(t('apps.unreachableHint'), {
                path: <span className="font-mono">/apis</span>,
                proxy: <span className="font-mono">frame-uiproxy</span>,
              })}
            </div>
          </CardContent>
        </Card>
      )}

      {state.phase === 'ready' && state.data.length === 0 && (
        <Card>
          <CardContent className="py-10 text-center font-mono text-sm text-muted-foreground">
            {t('apps.empty')}
          </CardContent>
        </Card>
      )}

      {state.phase === 'ready' &&
        state.data.map((app) => (
          <ApplicationCard key={`${app.namespace}/${app.name}`} app={app} onChanged={reload} />
        ))}
    </div>
  )
}

function ApplicationCard({ app, onChanged }: { app: Application; onChanged: () => void }) {
  const { t, tn } = useI18n()
  const pct = app.desiredReplicas > 0 ? (app.readyReplicas / app.desiredReplicas) * 100 : 0
  const [restartTarget, setRestartTarget] = useState<AppComponent | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [logsFor, setLogsFor] = useState<AppComponent | null>(null)
  // null while resolving, [] when the component has no pods -- two different
  // things to tell an operator, so they are not collapsed into one empty list.
  const [logPods, setLogPods] = useState<WorkloadPod[] | null>(null)
  const [logsError, setLogsError] = useState<string | null>(null)
  const [pod, setPod] = useState<WorkloadPod | null>(null)

  async function openLogs(c: AppComponent) {
    setLogsFor(c)
    setLogPods(null)
    setPod(null)
    setLogsError(null)
    try {
      const found = await frame.apps.pods(c)
      setLogPods(found)
      setPod(found[0] ?? null)
    } catch (e) {
      setLogsError(e instanceof Error ? e.message : String(e))
    }
  }

  async function doRestart(c: AppComponent) {
    setBusy(c.name)
    try {
      await frame.apps.restart(c)
      toast.success(t('apps.restarting', { name: c.name }))
      onChanged()
    } catch (e) {
      toast.error(t('apps.restartFailed', { name: c.name }), { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(null)
      setRestartTarget(null)
    }
  }

  async function doScale(c: AppComponent, delta: number) {
    const target = Math.max(0, c.desiredReplicas + delta)
    if (target === c.desiredReplicas) return
    setBusy(c.name)
    try {
      await frame.apps.scale(c, target)
      toast.success(t('apps.scaled', { name: c.name, n: target }))
      onChanged()
    } catch (e) {
      toast.error(t('apps.scaleFailed', { name: c.name }), { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(null)
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3 min-w-0">
            <Cube className="text-primary shrink-0" size={22} weight="duotone" />
            <div className="min-w-0">
              <CardTitle className="font-mono text-lg truncate">{app.name}</CardTitle>
              <p className="text-xs text-muted-foreground font-mono">
                {app.namespace} · {tn('apps.components', app.components.length)}
              </p>
            </div>
          </div>
          <Badge
            variant="outline"
            className={`font-mono ${TONE_TEXT[HEALTH_TONE[app.health]]} border-current`}
          >
            {app.health === 'healthy' ? (
              <CheckCircle className="mr-1" size={12} />
            ) : (
              <Warning className="mr-1" size={12} />
            )}
            {t(HEALTH_LABEL[app.health])}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1">
          <Progress value={pct} className="h-2" />
          <div className="flex justify-between text-xs text-muted-foreground font-mono">
            <span>{t('apps.replicasReady')}</span>
            <span>
              {app.readyReplicas} / {app.desiredReplicas}
            </span>
          </div>
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
          {app.components.map((c) => (
            <div
              key={`${c.namespace}/${c.name}`}
              className="p-3 rounded-lg border border-border bg-secondary/30 space-y-2"
            >
              <div className="flex items-center justify-between gap-2">
                <span className="font-mono text-xs font-bold truncate">{c.role ?? c.name}</span>
                <Badge variant="outline" className="text-[10px] font-mono shrink-0">
                  {c.kind}
                </Badge>
              </div>
              <div
                className={`font-mono text-sm font-bold ${
                  TONE_TEXT[HEALTH_TONE[healthTone(c.readyReplicas, c.desiredReplicas)]]
                }`}
              >
                {t('apps.ready', { ready: c.readyReplicas, desired: c.desiredReplicas })}
              </div>
              <div className="text-[10px] text-muted-foreground font-mono break-all">{c.image}</div>
              <div className="flex items-center justify-between gap-2 pt-1">
                <div className="flex items-center gap-1">
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-6 w-6 p-0"
                    disabled={busy === c.name}
                    onClick={() => doScale(c, -1)}
                  >
                    <Minus size={10} />
                  </Button>
                  <span className="font-mono text-[10px] w-4 text-center">{c.desiredReplicas}</span>
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-6 w-6 p-0"
                    disabled={busy === c.name}
                    onClick={() => doScale(c, 1)}
                  >
                    <Plus size={10} />
                  </Button>
                </div>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-6 font-mono text-[10px] gap-1"
                  onClick={() => void openLogs(c)}
                >
                  <FileText size={10} />
                  {t('apps.logs')}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-6 font-mono text-[10px] gap-1"
                  disabled={busy === c.name}
                  onClick={() => setRestartTarget(c)}
                >
                  <ArrowsClockwise className={busy === c.name ? 'animate-spin' : ''} size={10} />
                  {t('apps.restart')}
                </Button>
              </div>
            </div>
          ))}
        </div>
      </CardContent>

      <Dialog open={!!logsFor} onOpenChange={(open) => !open && setLogsFor(null)}>
        <DialogContent className="max-w-4xl">
          <DialogHeader>
            <DialogTitle className="font-mono">
              {logsFor?.name} {pod ? <span className="text-muted-foreground">· {pod.name}</span> : null}
            </DialogTitle>
            <DialogDescription>
              {t('apps.logsDesc')}
            </DialogDescription>
          </DialogHeader>

          {logsError && (
            <div className="font-mono text-xs text-destructive">{t('apps.podsFailed', { error: logsError })}</div>
          )}
          {!logsError && logPods === null && (
            <div className="font-mono text-xs text-muted-foreground">{t('apps.findingPods')}</div>
          )}
          {!logsError && logPods?.length === 0 && (
            <div className="font-mono text-xs text-muted-foreground">
              {t('apps.noPods', { kind: logsFor?.kind ?? '' })}
            </div>
          )}
          {logPods && logPods.length > 1 && (
            <div className="flex flex-wrap gap-1">
              {logPods.map((p) => (
                <Button
                  key={p.name}
                  variant={p.name === pod?.name ? 'default' : 'outline'}
                  size="sm"
                  className="h-6 font-mono text-[10px]"
                  onClick={() => setPod(p)}
                >
                  {p.name}
                </Button>
              ))}
            </div>
          )}
          {pod && <LogsTab pod={pod} />}
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!restartTarget} onOpenChange={(open) => !open && setRestartTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t('apps.restartTitle', { name: restartTarget?.name ?? '' })}</AlertDialogTitle>
            <AlertDialogDescription>
              {t('apps.restartDesc', { kind: restartTarget?.kind ?? '' })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction onClick={() => restartTarget && doRestart(restartTarget)}>
              {t('apps.restart')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

function healthTone(ready: number, desired: number): Application['health'] {
  if (desired === 0) return 'down'
  if (ready >= desired) return 'healthy'
  if (ready === 0) return 'down'
  return 'degraded'
}
