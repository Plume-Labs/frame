import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Application, AppComponent, createFrameClient } from '@/lib/frame-sdk'
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
} from '@phosphor-icons/react'

const frame = createFrameClient()

const HEALTH_TONE: Record<Application['health'], Tone> = {
  healthy: 'accent',
  degraded: 'warning',
  down: 'destructive',
}

const HEALTH_LABEL: Record<Application['health'], string> = {
  healthy: 'Healthy',
  degraded: 'Degraded',
  down: 'Down',
}

type LoadState =
  | { phase: 'loading' }
  | { phase: 'error'; message: string }
  | { phase: 'ready'; apps: Application[] }

export function ApplicationsView() {
  const [state, setState] = useState<LoadState>({ phase: 'loading' })

  const load = useCallback(async () => {
    setState({ phase: 'loading' })
    try {
      const apps = await frame.apps.list()
      setState({ phase: 'ready', apps })
    } catch (e) {
      setState({
        phase: 'error',
        message: e instanceof Error ? e.message : 'Failed to reach the Kubernetes API',
      })
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-mono text-xl flex items-center gap-2">
            <Package className="text-primary" />
            Applications
            <Button
              variant="outline"
              size="sm"
              className="ml-auto font-mono gap-1.5"
              onClick={() => void load()}
              disabled={state.phase === 'loading'}
            >
              <ArrowClockwise className={state.phase === 'loading' ? 'animate-spin' : ''} />
              Refresh
            </Button>
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Deployed workloads read live from the cluster, grouped by Helm release
            (<span className="font-mono">app.kubernetes.io/instance</span>).
          </p>
        </CardContent>
      </Card>

      {state.phase === 'loading' && (
        <Card>
          <CardContent className="py-10 text-center font-mono text-sm text-muted-foreground">
            Reading workloads from the Kubernetes API…
          </CardContent>
        </Card>
      )}

      {state.phase === 'error' && (
        <Card className="border-destructive/40">
          <CardContent className="py-8 space-y-3 text-center">
            <Warning className="mx-auto text-destructive" size={28} />
            <div className="font-mono text-sm text-foreground">Cannot reach the Kubernetes API</div>
            <div className="text-xs text-muted-foreground max-w-md mx-auto">{state.message}</div>
            <div className="text-xs text-muted-foreground">
              The UI proxies <span className="font-mono">/apis</span> to the apiserver via its
              ServiceAccount. In dev, run <span className="font-mono">kubectl proxy --port=8001</span>.
            </div>
          </CardContent>
        </Card>
      )}

      {state.phase === 'ready' && state.apps.length === 0 && (
        <Card>
          <CardContent className="py-10 text-center font-mono text-sm text-muted-foreground">
            No applications found outside the system namespaces.
          </CardContent>
        </Card>
      )}

      {state.phase === 'ready' &&
        state.apps.map((app) => (
          <ApplicationCard key={`${app.namespace}/${app.name}`} app={app} onChanged={() => void load()} />
        ))}
    </div>
  )
}

function ApplicationCard({ app, onChanged }: { app: Application; onChanged: () => void }) {
  const pct = app.desiredReplicas > 0 ? (app.readyReplicas / app.desiredReplicas) * 100 : 0
  const [restartTarget, setRestartTarget] = useState<AppComponent | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  async function doRestart(c: AppComponent) {
    setBusy(c.name)
    try {
      await frame.apps.restart(c)
      toast.success(`${c.name} restarting`)
      onChanged()
    } catch (e) {
      toast.error(`Failed to restart ${c.name}`, { description: e instanceof Error ? e.message : String(e) })
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
      toast.success(`${c.name} scaled to ${target}`)
      onChanged()
    } catch (e) {
      toast.error(`Failed to scale ${c.name}`, { description: e instanceof Error ? e.message : String(e) })
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
                {app.namespace} · {app.components.length} components
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
            {HEALTH_LABEL[app.health]}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1">
          <Progress value={pct} className="h-2" />
          <div className="flex justify-between text-xs text-muted-foreground font-mono">
            <span>Replicas ready</span>
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
                {c.readyReplicas}/{c.desiredReplicas} ready
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
                  disabled={busy === c.name}
                  onClick={() => setRestartTarget(c)}
                >
                  <ArrowsClockwise className={busy === c.name ? 'animate-spin' : ''} size={10} />
                  Restart
                </Button>
              </div>
            </div>
          ))}
        </div>
      </CardContent>

      <AlertDialog open={!!restartTarget} onOpenChange={(open) => !open && setRestartTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Restart {restartTarget?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Rolling-restarts every pod of this {restartTarget?.kind}. Brief capacity dip while new pods
              come up, no downtime if it has 2+ replicas with a working readiness probe.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => restartTarget && doRestart(restartTarget)}>
              Restart
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
