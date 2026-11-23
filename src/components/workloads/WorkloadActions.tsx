import { useState } from 'react'
import { toast } from 'sonner'

import { useI18n } from '@/i18n/context'
import { richText } from '@/i18n/rich'

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
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { PodSelection } from '@/components/workloads/PodDetailPanel'
import { createFrameClient } from '@/lib/frame-sdk'
import { canOperateWorkloads } from '@/lib/workloads'

const frame = createFrameClient()

type Pending = 'restart' | 'scale' | 'delete' | undefined

/**
 * Restart, scale and delete-a-pod, each behind a dialog that names the object
 * and the consequence.
 *
 * The delete dialog says up to three different things, because it can be three
 * different actions behind one button. Under a controller this screen
 * resolved — one of the four tracked kinds, placed by `buildWorkloadTree` — the
 * pod is replaced and the dialog names what will replace it. Under an owner
 * this screen could not resolve (an orphaned or hand-created ReplicaSet, or
 * any controller kind the tree does not track), the dialog says an owner
 * exists and the pod will most likely come back, without naming a controller
 * it cannot vouch for — that judgment comes from `pod.owner`, read straight
 * off the pod's own `ownerReferences`, not from tree placement, precisely so
 * an owned-but-unresolved pod is never told apart from a genuinely bare one.
 * On a truly bare pod — no `ownerReferences` at all — the deletion is final.
 * The person clicking has to know which of the three they have.
 *
 * Restart and Scale are not offered outside the namespaces where they are
 * granted, and the screen says why instead of leaving a button that returns
 * 403. The grant is a RoleBinding in each namespace enforcing `baseline` Pod
 * Security (`deploy/kubernetes/base/rbac-workload-operator.yaml`), because
 * patching a pod template is node root wherever a privileged pod is still
 * admitted — so the asymmetry is the security control, not a rollout gap, and
 * it will not go away.
 *
 * Delete pod is not gated: `pods delete` is a cluster-wide operator grant and
 * deleting a pod cannot mint a privileged one.
 */
export function WorkloadActions({
  selection,
  onChanged,
}: {
  selection: PodSelection
  onChanged: () => void
}) {
  const { t, tn } = useI18n()
  const { pod, controller } = selection
  const [pending, setPending] = useState<Pending>()
  const [replicas, setReplicas] = useState(String(controller?.desiredReplicas ?? 1))
  const [busy, setBusy] = useState(false)

  const run = async (what: string, fn: () => Promise<void>) => {
    setPending(undefined)
    setBusy(true)
    try {
      await fn()
      toast.success(what)
      onChanged()
    } catch (e) {
      toast.error(t('wl.act.failed', { what }), {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setBusy(false)
    }
  }

  // Whether the grant reaches this namespace at all, decided from the same
  // list the RoleBindings are generated from — not from "is this
  // infrastructure", which would offer the button in a namespace that is
  // merely unlabelled.
  const operable = canOperateWorkloads(pod.namespace)

  // DaemonSet deliberately excluded: cluster-control-workload-operator does
  // not grant `patch` on daemonsets (see the comment on that rule in
  // deploy/kubernetes/base/rbac-workload-operator.yaml — restart is offered
  // only for a Deployment or StatefulSet controller). Offering this button
  // for a DaemonSet would 403 for every operator, and only silently work for
  // an admin, whose tier does hold it — whole-branch review Important 2.
  const restartable =
    operable && controller && (controller.kind === 'Deployment' || controller.kind === 'StatefulSet')

  const target = Number(replicas)
  const targetValid = Number.isInteger(target) && target >= 0

  return (
    <div className="flex flex-wrap items-center gap-2">
      {!operable && controller && (
        <span className="font-mono text-[10px] text-muted-foreground max-w-md">
          {richText(t('wl.act.unavailable'), {
            ns: <strong>{pod.namespace}</strong>,
            baseline: <code>baseline</code>,
            kubectl: <code>kubectl</code>,
          })}
        </span>
      )}
      {restartable && (
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={busy}
          onClick={() => setPending('restart')}
        >
          {t('wl.act.restartKind', { kind: controller.kind.toLowerCase() })}
        </Button>
      )}
      {operable && controller?.scalable && (
        <Button
          size="sm"
          variant="outline"
          className="font-mono"
          disabled={busy}
          onClick={() => {
            // Seeded fresh at the moment the dialog opens, not left at
            // whatever it was set to the first time this component rendered:
            // `replicas` is local state and does not reinitialise itself when
            // `controller` changes underneath it, so a scale performed
            // earlier in this same panel session would otherwise leave the
            // input defaulted to the pre-scale count on a second open.
            setReplicas(String(controller.desiredReplicas))
            setPending('scale')
          }}
        >
          {t('wl.act.scale')}
        </Button>
      )}
      <Button
        size="sm"
        variant="outline"
        className="font-mono text-destructive"
        disabled={busy}
        onClick={() => setPending('delete')}
      >
        {t('wl.act.deletePod')}
      </Button>

      <AlertDialog open={pending === 'restart'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('wl.act.restartTitle', { kind: controller?.kind.toLowerCase() ?? '', target: `${pod.namespace}/${controller?.name}` })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t('wl.act.restartBody')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                controller &&
                restartable &&
                void run(t('wl.act.restarting', { name: controller.name }), () =>
                  frame.workloads.restart(
                    controller.kind as 'Deployment' | 'StatefulSet',
                    controller.namespace,
                    controller.name,
                  ),
                )
              }
            >
              {t('wl.act.restart')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={pending === 'scale'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('wl.act.scaleTitle', { kind: controller?.kind.toLowerCase() ?? '', target: `${pod.namespace}/${controller?.name}` })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {tn('wl.act.currently', controller?.desiredReplicas ?? 0)}{' '}
              {target === 0 ? t('wl.act.scaleZero') : t('wl.act.scaleApplied')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <Input
            value={replicas}
            inputMode="numeric"
            onChange={(e) => setReplicas(e.target.value)}
            className="font-mono text-xs w-24"
            aria-label={t('wl.act.replicas')}
          />
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              disabled={!targetValid}
              onClick={() =>
                controller?.scalable &&
                targetValid &&
                void run(t('wl.act.scaledTo', { name: controller.name, n: target }), () =>
                  frame.workloads.scale(
                    controller.kind as 'Deployment' | 'StatefulSet',
                    controller.namespace,
                    controller.name,
                    target,
                  ),
                )
              }
            >
              {target === 0 ? t('wl.act.stop') : t('wl.act.scale')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={pending === 'delete'} onOpenChange={(o) => !o && setPending(undefined)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t('wl.act.deleteTitle', { target: `${pod.namespace}/${pod.name}` })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {controller
                ? t('wl.act.deleteOwned', { kind: controller.kind, name: controller.name })
                : pod.owner
                  ? t('wl.act.deleteUnresolved', { kind: pod.owner.kind, name: pod.owner.name })
                  : t('wl.act.deleteBare')}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t('common.cancel')}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                void run(t('wl.act.deleted', { name: pod.name }), () =>
                  frame.workloads.deletePod(pod.namespace, pod.name),
                )
              }
            >
              {t('common.delete')}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
