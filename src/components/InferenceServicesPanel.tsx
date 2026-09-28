import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { ArrowClockwise, Play, Stop, Warning } from '@phosphor-icons/react'

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
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { useLiveResource } from '@/hooks/useLiveResource'
import { createFrameClient, type InferenceServiceView, type TaskRecord } from '@/lib/frame-sdk'
import {
  actionsFor,
  applyWarning,
  canConfirm,
  confirmationFor,
  formatDuration,
  lastTaskFor,
  permissionFromError,
  reasonMaxLength,
  replicasFor,
  sanitizeReason,
  type ActionKind,
  type Permission,
  type Permissions,
  type PodState,
} from '@/lib/inference-services'
import { MAX_ACTION_LENGTH } from '@/lib/manifest-diff'
import { canOperateWorkloads } from '@/lib/workloads'

const frame = createFrameClient()

const POD_LABEL: Record<PodState, { text: string; tone: string }> = {
  ready: { text: 'ready', tone: 'text-accent' },
  loading: { text: 'loading the model', tone: 'text-warning' },
  starting: { text: 'starting', tone: 'text-warning' },
  stopping: { text: 'stopping', tone: 'text-warning' },
  stopped: { text: 'stopped', tone: 'text-muted-foreground' },
}

const ICON: Record<ActionKind, typeof Play> = { restart: ArrowClockwise, stop: Stop, start: Play }
const LABEL: Record<ActionKind, string> = { restart: 'Restart', stop: 'Stop', start: 'Start' }

/**
 * The model servers — chat, vision, embeddings — with what they are doing and
 * the three writes lot 1 allows: restart, stop, start (spec §2.2, §5).
 *
 * Every rule (which Deployment, which buttons, what the dialog says, what it
 * takes to unlock it) is in `src/lib/inference-services.ts`, where it is tested.
 */
export function InferenceServicesPanel({ inFlight }: { inFlight?: number }) {
  const { state, reload } = useLiveResource<InferenceServiceView[]>(() => frame.inference.list(), [], [], 10_000)
  const { state: taskState } = useLiveResource<TaskRecord[]>(() => frame.tasks.list(100), [], [], 30_000)
  const tasks = taskState.phase === 'ready' ? taskState.data : []

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-mono text-lg">Model servers</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {state.phase === 'loading' && <p className="font-mono text-xs text-muted-foreground">Loading…</p>}
        {state.phase === 'error' && <p className="font-mono text-xs text-destructive">{state.message}</p>}
        {state.phase === 'ready' &&
          state.data.map((v) => (
            <ServiceRow
              key={v.def.key}
              view={v}
              tasks={tasks}
              inFlight={v.def.role === 'chat' ? inFlight : undefined}
              onChanged={reload}
            />
          ))}
        <p className="font-mono text-[10px] text-muted-foreground">
          The gateway and the endpoint picker have no buttons here: their fields belong to the gateway
          controller and to Helm.
        </p>
      </CardContent>
    </Card>
  )
}

function usePermissions(namespace: string, name: string | undefined): [Permissions, (p: Partial<Permissions>) => void] {
  const [perms, setPerms] = useState<Permissions>({ patch: { state: 'checking' }, scale: { state: 'checking' } })
  useEffect(() => {
    if (!name) return
    if (!canOperateWorkloads(namespace)) {
      const denied: Permission = {
        state: 'denied',
        reason: `Restart and scale are not granted in ${namespace}: it is not a namespace enforcing baseline Pod Security.`,
      }
      setPerms({ patch: denied, scale: denied })
      return
    }
    let live = true
    const probe = (what: 'patch' | 'scale', verb: string) =>
      frame.workloads.probeWrite(namespace, name, what).then(
        (): Permission => ({ state: 'allowed' }),
        (e): Permission => permissionFromError(e, verb, namespace),
      )
    void Promise.all([probe('patch', 'patch deployments'), probe('scale', 'patch deployments/scale')]).then(
      ([patch, scale]) => live && setPerms({ patch, scale }),
    )
    return () => {
      live = false
    }
  }, [namespace, name])
  return [perms, (p) => setPerms((cur) => ({ ...cur, ...p }))]
}

function ServiceRow({
  view,
  tasks,
  inFlight,
  onChanged,
}: {
  view: InferenceServiceView
  tasks: TaskRecord[]
  inFlight?: number
  onChanged: () => void
}) {
  const { def, namespace, deployment, state: s } = view
  const [perms, updatePerms] = usePermissions(namespace, deployment)
  const [pending, setPending] = useState<ActionKind>()
  const [typed, setTyped] = useState('')
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)

  const lastTask = deployment ? lastTaskFor(tasks, namespace, deployment) : undefined
  const confirmation =
    pending && deployment
      ? confirmationFor(pending, def, { namespace, name: deployment }, { lastLoadSeconds: s?.lastLoadSeconds, inFlight })
      : undefined

  const open = (a: ActionKind) => {
    setTyped('')
    setReason('')
    setPending(a)
  }

  const run = async (a: ActionKind) => {
    if (!deployment) return
    const why = reason
    setPending(undefined)
    setBusy(true)
    const what = `${def.label}: ${LABEL[a].toLowerCase()}`
    try {
      if (a === 'restart') await frame.workloads.restart('Deployment', namespace, deployment)
      else await frame.workloads.scale('Deployment', namespace, deployment, replicasFor(def, a), a === 'stop' ? why : undefined)
      toast.success(what)
      onChanged()
    } catch (e) {
      // A 403 here means the probe was wrong or the grant changed since: the
      // button goes grey with the reason instead of failing the same way twice.
      const p = permissionFromError(e, a === 'restart' ? 'patch deployments' : 'patch deployments/scale', namespace)
      if (p.state === 'denied') updatePerms(a === 'restart' ? { patch: p } : { scale: p })
      toast.error(`Failed: ${what}`, { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded border border-border bg-secondary/30 p-3 space-y-2">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <span className="font-mono text-sm font-bold">
          {def.label}
          {deployment && (
            <span className="ml-2 font-normal text-muted-foreground">
              {namespace}/{deployment}
            </span>
          )}
        </span>
        {s && (
          <span className={`font-mono text-xs ${POD_LABEL[s.pod].tone}`}>
            {POD_LABEL[s.pod].text} · {s.ready}/{s.desired} ready
          </span>
        )}
      </div>

      {view.unresolved && <p className="font-mono text-[10px] text-muted-foreground">{view.unresolved}</p>}
      {view.error && <p className="font-mono text-[10px] text-destructive">{view.error}</p>}

      {s && (
        <div className="flex flex-wrap gap-2 text-[10px]">
          {view.model && (
            <Badge variant="outline" className="font-mono">
              {view.model}
            </Badge>
          )}
          {view.contextSize !== undefined && (
            <Badge variant="outline" className="font-mono">
              ctx {view.contextSize}
            </Badge>
          )}
          {view.slots !== undefined && (
            <Badge variant="outline" className="font-mono">
              {view.slots} slots
            </Badge>
          )}
          {s.podStartedAt && (
            <Badge variant="outline" className="font-mono">
              pod since {new Date(s.podStartedAt).toLocaleString()}
            </Badge>
          )}
          {s.lastLoadSeconds !== undefined && (
            <Badge variant="outline" className="font-mono">
              last load {formatDuration(s.lastLoadSeconds)}
            </Badge>
          )}
          <Badge variant="outline" className="font-mono">
            last restart {s.lastRestart ? new Date(s.lastRestart).toLocaleString() : 'none recorded'}
          </Badge>
          {lastTask && (
            <Badge variant="outline" className="font-mono">
              last console action: {lastTask.action} by {lastTask.user} ({lastTask.phase})
            </Badge>
          )}
        </div>
      )}

      {s && deployment && (
        <>
          <div className="flex flex-wrap items-center gap-2">
            {actionsFor(s, perms).map((o) => {
              const Icon = ICON[o.kind]
              return (
                <div key={o.kind} className="flex items-center gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    className={`font-mono gap-1 ${o.kind === 'stop' ? 'text-destructive' : ''}`}
                    disabled={!o.enabled || busy}
                    title={o.why}
                    onClick={() => open(o.kind)}
                  >
                    <Icon size={14} />
                    {LABEL[o.kind]}
                  </Button>
                  {o.why && <span className="font-mono text-[10px] text-muted-foreground max-w-xs">{o.why}</span>}
                </div>
              )
            })}
          </div>
          <p className="flex gap-1 font-mono text-[10px] text-warning">
            <Warning size={12} className="shrink-0 mt-px" />
            {applyWarning(s)}
          </p>
        </>
      )}

      <AlertDialog open={!!confirmation} onOpenChange={(o) => !o && setPending(undefined)}>
        {confirmation && pending && (
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{confirmation.title}</AlertDialogTitle>
              <AlertDialogDescription className="space-y-2">
                <span className="block">{confirmation.effect}</span>
                <span className="block font-semibold">{confirmation.outage}</span>
                {pending === 'stop' && s && <span className="block">{applyWarning(s)}</span>}
              </AlertDialogDescription>
            </AlertDialogHeader>
            {confirmation.typeToConfirm !== undefined && (
              <Input
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                placeholder={`type ${confirmation.typeToConfirm} to confirm`}
                aria-label="Service name"
                className="font-mono text-xs"
              />
            )}
            {confirmation.requireReason && deployment && (
              <div className="space-y-1">
                <Textarea
                  value={reason}
                  maxLength={reasonMaxLength(namespace, deployment, MAX_ACTION_LENGTH)}
                  onChange={(e) => setReason(e.target.value)}
                  placeholder="Reason (recorded with the action)"
                  aria-label="Reason"
                  className="font-mono text-xs"
                />
                <p className="font-mono text-[10px] text-muted-foreground">
                  {reason.length}/{reasonMaxLength(namespace, deployment, MAX_ACTION_LENGTH)} · recorded as:{' '}
                  {sanitizeReason(reason) || '(empty — accents are folded, emoji and other symbols dropped)'}
                </p>
              </div>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction
                disabled={!canConfirm(confirmation, typed, reason)}
                onClick={() => canConfirm(confirmation, typed, reason) && void run(pending)}
              >
                {confirmation.confirmLabel}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        )}
      </AlertDialog>
    </div>
  )
}
